package controller

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/middleware"
	coremodel "github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay"
	"github.com/infinmalum/one-gateway/relay/ginmeta"
	"github.com/infinmalum/one-gateway/relay/lifecycle"
	"github.com/infinmalum/one-gateway/relay/native"
	"github.com/infinmalum/one-gateway/relay/relaymode"
)

// RelayProviderChat retains provider-specific wire adapters while routing Chat
// selection, retry, and billing through the shared lifecycle. The adapters' Gin
// transports are replaced in phase 5 along with the remaining legacy routes.
func RelayProviderChat(c *gin.Context) *lifecycle.HTTPError {
	body, err := common.GetRequestBody(c)
	if err != nil {
		return &lifecycle.HTTPError{Status: http.StatusBadRequest, Message: err.Error()}
	}
	parsed, err := getAndValidateTextRequest(c, relaymode.ChatCompletions)
	if err != nil {
		return &lifecycle.HTTPError{Status: http.StatusBadRequest, Message: err.Error()}
	}
	limit := int64(parsed.MaxTokens)
	if parsed.MaxCompletionTokens != nil {
		if *parsed.MaxCompletionTokens < 0 {
			return &lifecycle.HTTPError{Status: http.StatusBadRequest, Message: "max_completion_tokens must not be negative"}
		}
		limit = max(limit, int64(*parsed.MaxCompletionTokens))
	}
	metadata := ginmeta.Get(c)
	specificValue, pinned := c.Get(ctxkey.SpecificChannelId)
	specificID := 0
	if pinned {
		value, ok := specificValue.(string)
		if !ok {
			return &lifecycle.HTTPError{Status: http.StatusBadRequest, Message: "invalid channel ID"}
		}
		specificID, err = strconv.Atoi(value)
		if err != nil || specificID <= 0 {
			return &lifecycle.HTTPError{Status: http.StatusBadRequest, Message: "invalid channel ID"}
		}
	}
	input := lifecycle.Request{
		Protocol: native.OpenAIChat, Model: parsed.Model, Stream: parsed.Stream,
		MaxOutputTokens: limit, Body: body, RetryLimit: config.RetryTimes,
		Channel: lifecycle.Channel{ID: metadata.ChannelId, Type: metadata.ChannelType,
			BaseURL: metadata.BaseURL, APIKey: metadata.APIKey,
			ModelMapping: metadata.ModelMapping, SystemPrompt: metadata.ForcedSystemPrompt,
			APIVersion: metadata.Config.APIVersion},
		Principal: lifecycle.Principal{UserID: metadata.UserId, TokenID: metadata.TokenId,
			TokenName: metadata.TokenName, Group: metadata.Group,
			SpecificChannel: pinned, SpecificChannelID: specificID},
	}
	return lifecycle.ForwardAdapter(c.Request.Context(), input, func(channel lifecycle.Channel, mappedModel string) lifecycle.AdapterResult {
		if channel.ID != c.GetInt(ctxkey.ChannelId) {
			selected, err := coremodel.GetChannelById(channel.ID, true)
			if err != nil || selected == nil {
				return adapterFailure(http.StatusInternalServerError, "failed to load retry channel")
			}
			middleware.SetupContextForSelectedChannel(c, selected, parsed.Model)
		}
		meta := ginmeta.Get(c)
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		request, err := getAndValidateTextRequest(c, relaymode.ChatCompletions)
		if err != nil {
			return adapterFailure(http.StatusBadRequest, err.Error())
		}
		meta.IsStream = request.Stream
		meta.OriginModelName = request.Model
		request.Model = mappedModel
		meta.ActualModelName = mappedModel
		setSystemPrompt(c.Request.Context(), request, meta.ForcedSystemPrompt)
		meta.PromptTokens = getPromptTokens(request, relaymode.ChatCompletions)
		provider := relay.GetAdaptor(meta.APIType)
		if provider == nil {
			return adapterFailure(http.StatusUnprocessableEntity, "selected channel has no Chat adapter")
		}
		provider.Init(meta)
		requestBody, err := getRequestBody(c, meta, request, provider)
		if err != nil {
			return adapterFailure(http.StatusUnprocessableEntity, err.Error())
		}
		response, err := provider.DoRequest(c, meta, requestBody)
		if err != nil {
			return adapterFailure(http.StatusBadGateway, "upstream request failed")
		}
		if isErrorHappened(meta, response) {
			if response == nil {
				return adapterFailure(http.StatusBadGateway, "upstream response is missing")
			}
			upstream := lifecycle.ReadUpstreamError(response)
			return lifecycle.AdapterResult{Err: &lifecycle.HTTPError{Status: upstream.Status, Message: upstream.Message, Upstream: &upstream}}
		}
		usage, responseError := provider.DoResponse(c, response, meta)
		result := lifecycle.AdapterResult{OutputStarted: c.Writer.Written()}
		if usage != nil {
			result.Usage = native.Usage{Input: int64(usage.PromptTokens), Output: int64(usage.CompletionTokens), Seen: usage.TotalTokens > 0, Complete: responseError == nil}
		}
		if responseError != nil {
			result.Err = &lifecycle.HTTPError{Status: responseError.StatusCode, Message: responseError.Message}
			if result.Err.Status == 0 {
				result.Err.Status = http.StatusBadGateway
			}
		} else if !result.OutputStarted {
			result.Err = &lifecycle.HTTPError{Status: http.StatusBadGateway, Message: "provider adapter produced no response"}
		}
		return result
	})
}

func adapterFailure(status int, message string) lifecycle.AdapterResult {
	return lifecycle.AdapterResult{Err: &lifecycle.HTTPError{Status: status, Message: message}}
}
