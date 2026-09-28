package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/relay/ginmeta"
	"github.com/infinmalum/one-gateway/relay/lifecycle"
	"github.com/infinmalum/one-gateway/relay/native"
)

type nativeInput struct {
	protocol                  native.Protocol
	upstreamProtocol          native.Protocol
	fallbackUpstreamProtocols []native.Protocol
	model                     string
	action                    string
	stream                    bool
	maxOutputTokens           int64
	fallbackInputTokens       int64
	body                      []byte
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

// NativeOpenAIChat forwards OpenAI channels without the legacy Chat adapter.
// Other channel types still use their existing conversion path.
func NativeOpenAIChat(c *gin.Context) {
	nativeOpenAIChat(c, native.OpenAIChat)
}

func NativeOpenAIChatViaAnthropic(c *gin.Context) {
	nativeOpenAIChat(c, native.Anthropic)
}

func NativeOpenAIChatViaGemini(c *gin.Context) {
	nativeOpenAIChat(c, native.Gemini)
}

func nativeOpenAIChat(c *gin.Context, upstream native.Protocol) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIChat, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model         string          `json:"model"`
		Messages      json.RawMessage `json:"messages"`
		Stream        bool            `json:"stream"`
		MaxTokens     int64           `json:"max_tokens"`
		MaxCompletion int64           `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || fields.Model == "" || len(fields.Messages) == 0 || fields.MaxTokens < 0 || fields.MaxCompletion < 0 {
		writeNativeError(c, native.OpenAIChat, http.StatusBadRequest, errors.New("model and messages are required; token limits must not be negative"))
		return
	}
	limit := max(fields.MaxTokens, fields.MaxCompletion)
	if upstream == native.Anthropic && limit == 0 {
		limit = 1024
	}
	// The Gemini upstream action is derived from the stream flag in the
	// lifecycle, which knows the final upstream protocol.
	forwardNative(c, nativeInput{protocol: native.OpenAIChat, upstreamProtocol: upstream, model: fields.Model, stream: fields.Stream,
		maxOutputTokens: limit, body: body})
}

func NativeOpenAIEmbeddings(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIEmbeddings, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || len(fields.Input) == 0 || string(fields.Input) == "null" {
		writeNativeError(c, native.OpenAIEmbeddings, http.StatusBadRequest, errors.New("model and input are required"))
		return
	}
	if c.Param("model") != "" {
		if fields.Model != "" && fields.Model != c.Param("model") {
			writeNativeError(c, native.OpenAIEmbeddings, http.StatusBadRequest, errors.New("body model does not match the engine path"))
			return
		}
		fields.Model = c.Param("model")
		if len(body) > 0 {
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
				writeNativeError(c, native.OpenAIEmbeddings, http.StatusBadRequest, errors.New("invalid embeddings request"))
				return
			}
			payload["model"], _ = json.Marshal(fields.Model)
			body, _ = json.Marshal(payload)
		}
	}
	if fields.Model == "" {
		writeNativeError(c, native.OpenAIEmbeddings, http.StatusBadRequest, errors.New("model is required"))
		return
	}
	// Embeddings have no output-token limit. Reserve against the input byte
	// length so an unusually large response without parseable usage cannot
	// leave only the small default reservation charged.
	forwardNative(c, nativeInput{protocol: native.OpenAIEmbeddings, model: fields.Model,
		maxOutputTokens: int64(len(body)), body: body})
}

func NativeOpenAIModerations(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIModerations, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || len(fields.Input) == 0 || string(fields.Input) == "null" {
		writeNativeError(c, native.OpenAIModerations, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	if fields.Model == "" {
		fields.Model = native.DefaultModerationModel
	}
	forwardNative(c, nativeInput{protocol: native.OpenAIModerations, model: fields.Model,
		fallbackInputTokens: native.EstimateModerationInputTokens(fields.Input), body: body})
}

func NativeOpenAICompletions(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAICompletions, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model     string          `json:"model"`
		Prompt    json.RawMessage `json:"prompt"`
		Stream    bool            `json:"stream"`
		MaxTokens int64           `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || fields.Model == "" || len(fields.Prompt) == 0 || string(fields.Prompt) == "null" || fields.MaxTokens < 0 {
		writeNativeError(c, native.OpenAICompletions, http.StatusBadRequest, errors.New("model and prompt are required; max_tokens must not be negative"))
		return
	}
	forwardNative(c, nativeInput{protocol: native.OpenAICompletions, model: fields.Model,
		stream: fields.Stream, maxOutputTokens: fields.MaxTokens, body: body})
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
	input := nativeInput{protocol: native.Anthropic, model: fields.Model, stream: fields.Stream,
		maxOutputTokens: fields.MaxTokens, body: body,
		fallbackUpstreamProtocols: []native.Protocol{native.OpenAIChat, native.Gemini}}
	forwardNative(c, input)
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
	metadata := ginmeta.Get(c)
	version := ""
	if input.protocol == native.Gemini {
		version = strings.SplitN(strings.TrimPrefix(c.Request.URL.Path, "/"), "/", 2)[0]
	}
	specificValue, specificChannel := c.Get(ctxkey.SpecificChannelId)
	specificChannelID := 0
	if specificChannel {
		var err error
		specificChannelID, err = strconv.Atoi(specificValue.(string))
		if err != nil || specificChannelID <= 0 {
			writeNativeError(c, input.protocol, http.StatusBadRequest, errors.New("invalid channel ID"))
			return
		}
	}
	result := lifecycle.Forward(c.Request.Context(), c.Writer, lifecycle.Request{
		Protocol: input.protocol, UpstreamProtocol: input.upstreamProtocol,
		FallbackUpstreamProtocols: input.fallbackUpstreamProtocols, Model: input.model, Action: input.action,
		Version: version, Stream: input.stream, MaxOutputTokens: input.maxOutputTokens,
		FallbackInputTokens: input.fallbackInputTokens,
		Body:                input.body, Headers: c.Request.Header, Query: c.Request.URL.Query(),
		Channel: lifecycle.Channel{
			ID: metadata.ChannelId, Type: metadata.ChannelType,
			BaseURL: metadata.BaseURL, APIKey: metadata.APIKey,
			ModelMapping: metadata.ModelMapping, SystemPrompt: metadata.ForcedSystemPrompt,
			APIVersion: metadata.Config.APIVersion,
		},
		Principal: lifecycle.Principal{
			UserID: metadata.UserId, TokenID: metadata.TokenId,
			TokenName: metadata.TokenName, Group: metadata.Group,
			SpecificChannel: specificChannel, SpecificChannelID: specificChannelID,
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
