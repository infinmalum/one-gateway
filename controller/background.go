package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/relay/background"
	"github.com/infinmalum/one-gateway/relay/ginmeta"
	"github.com/infinmalum/one-gateway/relay/lifecycle"
	"github.com/infinmalum/one-gateway/relay/native"
)

// startBackgroundResponses validates channel availability synchronously, then
// executes the request in a detached goroutine and returns the queued response
// object immediately. Channel and quota problems that surface during execution
// are reported through retrieval with a failed status.
//
// The upstream endpoint comes from the admin-configured channel only; the
// request carries the body and an explicit allowlist of protocol headers, and
// native.BuildRequest rejects any base URL outside http(s).
func startBackgroundResponses(c *gin.Context, model string, maxOutputTokens int64, body []byte) {
	metadata := ginmeta.Get(c)
	pinned, specificChannelID, err := specificChannelOf(c)
	if err != nil {
		writeNativeError(c, native.OpenAIResponses, http.StatusBadRequest, err)
		return
	}
	input := lifecycle.Request{
		Protocol: native.OpenAIResponses, Model: model,
		MaxOutputTokens: maxOutputTokens, Body: body,
		Headers: forwardedProtocolHeaders(c),
		Channel: lifecycle.Channel{
			ID: metadata.ChannelId, Type: metadata.ChannelType,
			BaseURL: metadata.BaseURL, APIKey: metadata.APIKey,
			ModelMapping: metadata.ModelMapping, SystemPrompt: metadata.ForcedSystemPrompt,
			APIVersion: metadata.Config.APIVersion,
		},
		Principal: lifecycle.Principal{
			UserID: metadata.UserId, TokenID: metadata.TokenId,
			TokenName: metadata.TokenName, Group: metadata.Group,
			SpecificChannel: pinned, SpecificChannelID: specificChannelID,
		},
		RetryLimit: config.RetryTimes,
	}
	if _, selectErr := lifecycle.SelectForRequest(input, native.OpenAIResponses); selectErr != nil {
		writeNativeError(c, native.OpenAIResponses, selectErr.Status, errors.New(selectErr.Message))
		return
	}
	entry := background.Default().Start(c.Request.Context(), metadata.UserId, model, forwardIntoBuffer(input))
	_, payload := entry.Snapshot()
	c.Data(http.StatusAccepted, "application/json", payload)
}

// forwardIntoBuffer wraps the shared forwarding step so background entries
// capture what would have been written to the client.
func forwardIntoBuffer(input lifecycle.Request) background.Runner {
	return func(ctx context.Context, dst http.ResponseWriter) {
		result := lifecycle.Forward(ctx, dst, input)
		if result != nil {
			writeForwardError(dst, native.OpenAIResponses, result)
		}
	}
}

// forwardedProtocolHeaders copies the few header values the native transport
// substitutes upstream; everything else stays client-local.
func forwardedProtocolHeaders(c *gin.Context) http.Header {
	headers := http.Header{}
	for _, name := range []string{"OpenAI-Beta", "Anthropic-Version", "Anthropic-Beta", "Anthropic-Workspace-Id"} {
		if value := c.GetHeader(name); value != "" {
			headers.Set(name, value)
		}
	}
	return headers
}

func specificChannelOf(c *gin.Context) (bool, int, error) {
	specificValue, pinned := c.Get(ctxkey.SpecificChannelId)
	if !pinned {
		return false, 0, nil
	}
	value, ok := specificValue.(string)
	if !ok {
		return true, 0, errors.New("invalid channel ID")
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return true, 0, errors.New("invalid channel ID")
	}
	return true, id, nil
}

// writeForwardError renders a lifecycle error that never reached the client.
func writeForwardError(dst http.ResponseWriter, protocol native.Protocol, result *lifecycle.HTTPError) {
	var body any
	if result.Upstream != nil {
		body = native.ErrorBodyWithUpstream(protocol, result.Status, result.Message,
			result.Upstream.Type, result.Upstream.Code, result.Upstream.Param)
	} else {
		body = native.ErrorBody(protocol, result.Status, result.Message)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		payload = []byte(`{"error":{"message":"request failed"}}`)
	}
	dst.Header().Set("Content-Type", "application/json")
	dst.WriteHeader(result.Status)
	_, _ = dst.Write(payload)
}

// backgroundResponseForCaller resolves the route's response ID to a stored
// entry owned by the authenticated user. Unknown IDs and other users'
// responses are indistinguishable so ownership is not disclosed.
func backgroundResponseForCaller(c *gin.Context) (*background.Entry, bool) {
	responseID := c.Param("responseId")
	entry := background.Default().Lookup(responseID)
	if entry == nil {
		return nil, false
	}
	if entry.Owner() != ginmeta.Get(c).UserId {
		return nil, false
	}
	return entry, true
}

// NativeOpenAIResponsesRetrieve serves a stored background response object.
func NativeOpenAIResponsesRetrieve(c *gin.Context) {
	entry, found := backgroundResponseForCaller(c)
	if !found {
		writeNativeError(c, native.OpenAIResponses, http.StatusNotFound, errors.New("response not found"))
		return
	}
	status, payload := entry.Snapshot()
	c.Data(status, "application/json", payload)
}

// NativeOpenAIResponsesCancel cancels a background response. Cancelling a
// finished response returns its final object unchanged.
func NativeOpenAIResponsesCancel(c *gin.Context) {
	entry, found := backgroundResponseForCaller(c)
	if !found {
		writeNativeError(c, native.OpenAIResponses, http.StatusNotFound, errors.New("response not found"))
		return
	}
	entry.Cancel()
	_, payload := entry.Snapshot()
	c.Data(http.StatusOK, "application/json", payload)
}
