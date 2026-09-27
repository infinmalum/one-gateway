package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/relay/lifecycle"
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

func NativeOpenAIResponses(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIResponses, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model           string `json:"model"`
		Stream          bool   `json:"stream"`
		Background      bool   `json:"background"`
		MaxOutputTokens int64  `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || fields.Model == "" || fields.MaxOutputTokens < 0 {
		writeNativeError(c, native.OpenAIResponses, http.StatusBadRequest, errors.New("model is required and max_output_tokens must not be negative"))
		return
	}
	if fields.Background {
		writeNativeError(c, native.OpenAIResponses, http.StatusUnprocessableEntity, errors.New("background Responses require retrieval and cancellation endpoints"))
		return
	}
	forwardNative(c, nativeInput{protocol: native.OpenAIResponses, model: fields.Model, stream: fields.Stream, maxOutputTokens: fields.MaxOutputTokens, body: body})
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
		writeNativeError(c, native.Gemini, http.StatusUnprocessableEntity, errors.New("Gemini action is not supported"))
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
	metadata := meta.GetByContext(c)
	version := ""
	if input.protocol == native.Gemini {
		version = strings.SplitN(strings.TrimPrefix(c.Request.URL.Path, "/"), "/", 2)[0]
	}
	_, specificChannel := c.Get(ctxkey.SpecificChannelId)
	result := lifecycle.Forward(c.Request.Context(), c.Writer, lifecycle.Request{
		Protocol: input.protocol, Model: input.model, Action: input.action,
		Version: version, Stream: input.stream, MaxOutputTokens: input.maxOutputTokens,
		Body: input.body, Headers: c.Request.Header, Query: c.Request.URL.Query(),
		Channel: lifecycle.Channel{
			ID: metadata.ChannelId, Type: metadata.ChannelType,
			BaseURL: metadata.BaseURL, APIKey: metadata.APIKey,
			ModelMapping: metadata.ModelMapping, SystemPrompt: metadata.ForcedSystemPrompt,
			APIVersion: metadata.Config.APIVersion,
		},
		Principal: lifecycle.Principal{
			UserID: metadata.UserId, TokenID: metadata.TokenId,
			TokenName: metadata.TokenName, Group: metadata.Group,
			SpecificChannel: specificChannel,
		},
		RetryLimit: config.RetryTimes,
	})
	if result != nil {
		writeNativeError(c, input.protocol, result.Status, errors.New(result.Message))
	}
}

func writeNativeError(c *gin.Context, protocol native.Protocol, status int, err error) {
	c.JSON(status, native.ErrorBody(protocol, status, err.Error()))
}
