package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/common/helper"
	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/middleware"
	"github.com/infinmalum/one-gateway/monitor"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"github.com/infinmalum/one-gateway/relay/controller"
	"github.com/infinmalum/one-gateway/relay/lifecycle"
	"github.com/infinmalum/one-gateway/relay/model"
	"github.com/infinmalum/one-gateway/relay/native"
	"github.com/infinmalum/one-gateway/relay/relaymode"
)

// openAIWireChannel reports whether the channel selected by the distributor
// accepts the OpenAI JSON wire format, so the route can use the native
// lifecycle instead of a provider adapter.
func openAIWireChannel(c *gin.Context) bool {
	return channeltype.NativeChatCompatible(c.GetInt(ctxkey.Channel))
}

func unsupportedOperation(c *gin.Context, message string) {
	c.JSON(http.StatusUnprocessableEntity, native.ErrorBody(native.OpenAIChat, http.StatusUnprocessableEntity, message))
}

// RelayChat dispatches Chat Completions onto the native lifecycle for OpenAI
// wire, Anthropic, and Gemini channels, and onto the provider adapters for
// their channel types.
func RelayChat(c *gin.Context) {
	channelType := c.GetInt(ctxkey.Channel)
	switch {
	case channeltype.NativeChatCompatible(channelType):
		NativeOpenAIChat(c)
	case channelType == channeltype.Anthropic:
		NativeOpenAIChatViaAnthropic(c)
	case channelType == channeltype.Gemini:
		NativeOpenAIChatViaGemini(c)
	default:
		result := controller.RelayProviderChat(c)
		if result != nil && !c.Writer.Written() {
			if result.Upstream != nil {
				c.JSON(result.Status, native.ErrorBodyWithUpstream(native.OpenAIChat, result.Status, result.Message,
					result.Upstream.Type, result.Upstream.Code, result.Upstream.Param))
			} else {
				c.JSON(result.Status, native.ErrorBody(native.OpenAIChat, result.Status, result.Message))
			}
		}
	}
}

// RelayCompletions serves OpenAI wire channels through the native lifecycle;
// provider channels keep their adapter conversions.
func RelayCompletions(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAICompletions(c)
		return
	}
	runLegacyRelay(c, relaymode.Completions)
}

// RelayEmbeddings serves OpenAI wire channels natively; provider channels with
// their own embeddings formats keep their adapter conversions.
func RelayEmbeddings(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAIEmbeddings(c)
		return
	}
	runLegacyRelay(c, relaymode.Embeddings)
}

// RelayModerations serves OpenAI wire channels natively and rejects provider
// channels, whose adapters have no moderation format to convert into.
func RelayModerations(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAIModerations(c)
		return
	}
	unsupportedOperation(c, "moderations require an OpenAI-compatible channel")
}

// RelayEdits serves OpenAI wire channels natively and rejects provider
// channels; no provider adapter ever converted the deprecated edits format.
func RelayEdits(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAIEdits(c)
		return
	}
	unsupportedOperation(c, "edits require an OpenAI-compatible channel")
}

// RelayImages serves OpenAI wire channels through the native lifecycle with
// image-specific billing; provider image formats keep their adapters.
func RelayImages(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAIImages(c)
		return
	}
	runLegacyRelay(c, relaymode.ImagesGenerations)
}

// RelayAudio serves the three audio operations on OpenAI wire channels,
// including their Azure deployment variants, and rejects provider channels:
// the legacy audio path only ever spoke the OpenAI wire format.
func RelayAudio(c *gin.Context) {
	if !openAIWireChannel(c) {
		unsupportedOperation(c, "audio operations require an OpenAI-compatible channel")
		return
	}
	switch {
	case strings.HasSuffix(c.Request.URL.Path, "/audio/speech"):
		NativeOpenAIAudioSpeech(c)
	case strings.HasSuffix(c.Request.URL.Path, "/audio/transcriptions"):
		NativeOpenAIAudioTranscriptions(c)
	default:
		NativeOpenAIAudioTranslations(c)
	}
}

// legacyRelayModes are the endpoint families whose provider-specific adapters
// still serve non-wire channels during the adapter extraction.
func relayHelper(c *gin.Context, relayMode int) *model.ErrorWithStatusCode {
	switch relayMode {
	case relaymode.ImagesGenerations:
		return controller.RelayImageHelper(c, relayMode)
	default:
		return controller.RelayTextHelper(c)
	}
}

// runLegacyRelay executes the retained provider adapter paths with the legacy
// retry loop: it selects another matching channel after a retryable failure
// that happened before any response byte reached the client.
func runLegacyRelay(c *gin.Context, relayMode int) {
	ctx := c.Request.Context()
	if config.DebugEnabled {
		requestBody, _ := common.GetRequestBody(c)
		logger.Debugf(ctx, "request body: %s", string(requestBody))
	}
	channelId := c.GetInt(ctxkey.ChannelId)
	userId := c.GetInt(ctxkey.Id)
	bizErr := relayHelper(c, relayMode)
	if bizErr == nil {
		monitor.Emit(channelId, true)
		return
	}
	tried := map[int]bool{channelId: true}
	channelName := c.GetString(ctxkey.ChannelName)
	group := c.GetString(ctxkey.Group)
	originalModel := c.GetString(ctxkey.OriginalModel)
	go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
	requestId := c.GetString(helper.RequestIdKey)
	retryTimes := config.RetryTimes
	if !shouldRetry(c, bizErr.StatusCode) {
		logger.Errorf(ctx, "relay error happen, status code is %d, won't retry in this case", bizErr.StatusCode)
		retryTimes = 0
	}
	for i := retryTimes; i > 0; i-- {
		channel, selectErr := lifecycle.SelectChannel(lifecycle.Selection{
			Group: group, Model: originalModel, Type: -1, IgnorePriority: i != retryTimes, Excluded: tried,
		})
		if selectErr != nil {
			logger.Errorf(ctx, "retry channel selection failed: %s", selectErr.Message)
			break
		}
		logger.Infof(ctx, "using channel #%d to retry (remain times %d)", channel.Id, i)
		tried[channel.Id] = true
		middleware.SetupContextForSelectedChannel(c, channel, originalModel)
		requestBody, err := common.GetRequestBody(c)
		if err != nil {
			logger.Errorf(ctx, "failed to replay request body: %v", err)
			break
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
		bizErr = relayHelper(c, relayMode)
		if bizErr == nil {
			monitor.Emit(c.GetInt(ctxkey.ChannelId), true)
			return
		}
		channelId := c.GetInt(ctxkey.ChannelId)
		channelName := c.GetString(ctxkey.ChannelName)
		go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
		if !shouldRetry(c, bizErr.StatusCode) {
			break
		}
	}
	if bizErr != nil {
		if c.Writer.Written() {
			return
		}
		if bizErr.StatusCode == http.StatusTooManyRequests {
			bizErr.Error.Message = "当前分组上游负载已饱和，请稍后再试"
		}
		bizErr.Error.Message = helper.MessageWithRequestId(bizErr.Error.Message, requestId)
		c.JSON(bizErr.StatusCode, gin.H{
			"error": bizErr.Error,
		})
	}
}

func shouldRetry(c *gin.Context, statusCode int) bool {
	_, pinned := c.Get(ctxkey.SpecificChannelId)
	return lifecycle.Retryable(statusCode, c.Writer.Written(), pinned, c.Request.Context().Err() != nil)
}

func processChannelRelayError(ctx context.Context, userId int, channelId int, channelName string, err model.ErrorWithStatusCode) {
	logger.Errorf(ctx, "relay error (channel id %d, user id %d): %s", channelId, userId, err.Message)
	// https://platform.openai.com/docs/guides/error-codes/api-errors
	if monitor.ShouldDisableChannel(&err.Error, err.StatusCode) {
		monitor.DisableChannel(channelId, channelName, err.Message)
	} else {
		monitor.Emit(channelId, false)
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := model.Error{
		Message: "API not implemented",
		Type:    "one_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := model.Error{
		Message: formatInvalidURL(c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func formatInvalidURL(method, path string) string {
	return "Invalid URL (" + method + " " + path + ")"
}
