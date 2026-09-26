package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/relay/billing"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/native"
)

type nativeInput struct {
	protocol        native.Protocol
	model           string
	action          string
	stream          bool
	maxOutputTokens int64
	body            []byte
}

func NativeAnthropic(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.Anthropic, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model     string          `json:"model"`
		Messages  json.RawMessage `json:"messages"`
		MaxTokens int64           `json:"max_tokens"`
		Stream    bool            `json:"stream"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || fields.Model == "" || len(fields.Messages) == 0 || fields.MaxTokens <= 0 {
		writeNativeError(c, native.Anthropic, http.StatusBadRequest, errors.New("model, messages, and positive max_tokens are required"))
		return
	}
	forwardNative(c, nativeInput{protocol: native.Anthropic, model: fields.Model, stream: fields.Stream, maxOutputTokens: fields.MaxTokens, body: body})
}

func NativeGemini(c *gin.Context) {
	modelAction := c.Param("modelAction")
	separator := strings.LastIndexByte(modelAction, ':')
	if separator <= 0 {
		writeNativeError(c, native.Gemini, http.StatusBadRequest, errors.New("Gemini model and action are required"))
		return
	}
	modelName, action := modelAction[:separator], modelAction[separator+1:]
	if action != "generateContent" && action != "streamGenerateContent" {
		writeNativeError(c, native.Gemini, http.StatusNotImplemented, errors.New("Gemini action is not supported"))
		return
	}
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.Gemini, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Contents         json.RawMessage `json:"contents"`
		GenerationConfig struct {
			MaxOutputTokens int64 `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || len(fields.Contents) == 0 || fields.GenerationConfig.MaxOutputTokens < 0 {
		writeNativeError(c, native.Gemini, http.StatusBadRequest, errors.New("Gemini contents are required and maxOutputTokens must not be negative"))
		return
	}
	forwardNative(c, nativeInput{protocol: native.Gemini, model: modelName, action: action, stream: action == "streamGenerateContent", maxOutputTokens: fields.GenerationConfig.MaxOutputTokens, body: body})
}

func forwardNative(c *gin.Context, input nativeInput) {
	ctx := c.Request.Context()
	metadata := meta.GetByContext(c)
	if metadata.ForcedSystemPrompt != "" {
		writeNativeError(c, input.protocol, http.StatusUnprocessableEntity, errors.New("channel system prompt is not supported by native passthrough"))
		return
	}
	mappedModel := input.model
	if replacement := metadata.ModelMapping[input.model]; replacement != "" {
		mappedModel = replacement
	}
	version := ""
	if input.protocol == native.Gemini {
		version = strings.SplitN(strings.TrimPrefix(c.Request.URL.Path, "/"), "/", 2)[0]
		if metadata.Config.APIVersion != "" {
			version = metadata.Config.APIVersion
		}
	}
	req, err := native.BuildRequest(ctx, native.Request{
		Protocol: input.protocol, BaseURL: metadata.BaseURL, Version: version,
		Model: mappedModel, Action: input.action, APIKey: metadata.APIKey,
		Body: input.body, Headers: c.Request.Header, Query: c.Request.URL.Query(),
	})
	if err != nil {
		writeNativeError(c, input.protocol, http.StatusBadRequest, err)
		return
	}
	reservation, err := billing.ReserveNativeQuota(ctx, billing.NativeReservation{
		UserID: metadata.UserId, TokenID: metadata.TokenId, ChannelID: metadata.ChannelId,
		ChannelType: metadata.ChannelType, TokenName: metadata.TokenName,
		ModelName: mappedModel, Group: metadata.Group,
	}, input.maxOutputTokens)
	if err != nil {
		writeNativeError(c, input.protocol, http.StatusForbidden, err)
		return
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(req)
	settlementContext := context.WithoutCancel(ctx)
	if err != nil {
		reservation.Refund(settlementContext)
		logger.Errorf(settlementContext, "native upstream request failed: %v", err)
		writeNativeError(c, input.protocol, http.StatusBadGateway, errors.New("upstream request failed"))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		reservation.Refund(settlementContext)
		copyNativeHeaders(c.Writer.Header(), response.Header)
		c.Status(response.StatusCode)
		_, _ = io.Copy(c.Writer, response.Body)
		return
	}
	if input.stream && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		reservation.Refund(settlementContext)
		writeNativeError(c, input.protocol, http.StatusBadGateway, errors.New("upstream did not return an event stream"))
		return
	}
	copyNativeHeaders(c.Writer.Header(), response.Header)
	c.Status(response.StatusCode)
	usage, copyErr := native.CopyResponse(c.Writer, response.Body, input.protocol, input.stream)
	interrupted := copyErr != nil || ctx.Err() != nil || (input.stream && !usage.Complete)
	reservation.Settle(settlementContext, usage, input.stream, interrupted)
	if copyErr != nil {
		logger.Errorf(settlementContext, "native upstream response interrupted: %v", copyErr)
	}
}

func copyNativeHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Request-Id", "Retry-After", "Anthropic-Version", "X-Goog-Request-Id"} {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
}

func writeNativeError(c *gin.Context, protocol native.Protocol, status int, err error) {
	if protocol == native.Anthropic {
		kind := "invalid_request_error"
		if status >= 500 {
			kind = "api_error"
		}
		c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "message": err.Error()}})
		return
	}
	statusName := "INVALID_ARGUMENT"
	if status >= 500 {
		statusName = "UNAVAILABLE"
	}
	c.JSON(status, gin.H{"error": gin.H{"code": status, "message": err.Error(), "status": statusName}})
}
