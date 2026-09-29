package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

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

// https://platform.openai.com/docs/api-reference/chat

func relayHelper(c *gin.Context, relayMode int) *model.ErrorWithStatusCode {
	var err *model.ErrorWithStatusCode
	switch relayMode {
	case relaymode.ImagesGenerations:
		err = controller.RelayImageHelper(c, relayMode)
	case relaymode.AudioSpeech:
		fallthrough
	case relaymode.AudioTranslation:
		fallthrough
	case relaymode.AudioTranscription:
		err = controller.RelayAudioHelper(c, relayMode)
	case relaymode.Proxy:
		err = controller.RelayProxyHelper(c, relayMode)
	default:
		err = controller.RelayTextHelper(c)
	}
	return err
}

func Relay(c *gin.Context) {
	ctx := c.Request.Context()
	relayMode := relaymode.GetByPath(c.Request.URL.Path)
	if relayMode == relaymode.ChatCompletions && channeltype.NativeChatCompatible(c.GetInt(ctxkey.Channel)) {
		NativeOpenAIChat(c)
		return
	}
	if relayMode == relaymode.ChatCompletions && c.GetInt(ctxkey.Channel) == channeltype.Anthropic {
		NativeOpenAIChatViaAnthropic(c)
		return
	}
	if relayMode == relaymode.ChatCompletions && c.GetInt(ctxkey.Channel) == channeltype.Gemini {
		NativeOpenAIChatViaGemini(c)
		return
	}
	if relayMode == relaymode.ChatCompletions {
		result := controller.RelayProviderChat(c)
		if result != nil && !c.Writer.Written() {
			if result.Upstream != nil {
				c.JSON(result.Status, native.ErrorBodyWithUpstream(native.OpenAIChat, result.Status, result.Message,
					result.Upstream.Type, result.Upstream.Code, result.Upstream.Param))
			} else {
				c.JSON(result.Status, native.ErrorBody(native.OpenAIChat, result.Status, result.Message))
			}
		}
		return
	}
	if relayMode == relaymode.Embeddings && c.GetInt(ctxkey.Channel) == channeltype.OpenAI {
		NativeOpenAIEmbeddings(c)
		return
	}
	if relayMode == relaymode.Moderations && c.GetInt(ctxkey.Channel) == channeltype.OpenAI {
		NativeOpenAIModerations(c)
		return
	}
	if relayMode == relaymode.Completions && c.GetInt(ctxkey.Channel) == channeltype.OpenAI && c.GetString(ctxkey.SystemPrompt) == "" {
		NativeOpenAICompletions(c)
		return
	}
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

		// BUG: bizErr is in race condition
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
	logger.Errorf(ctx, "relay error (channel id %d, user id: %d): %s", channelId, userId, err.Message)
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
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}
