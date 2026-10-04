package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/relay/adaptor/openai"
	billingratio "github.com/infinmalum/one-gateway/relay/billing/ratio"
	"github.com/infinmalum/one-gateway/relay/ginmeta"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/native"
)

// NativeOpenAIEdits forwards the deprecated Edits wire format on channels that
// speak the OpenAI JSON protocol.
func NativeOpenAIEdits(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIEdits, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model       string `json:"model"`
		Instruction string `json:"instruction"`
		Stream      bool   `json:"stream"`
		MaxTokens   int64  `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || fields.Model == "" || fields.Instruction == "" || fields.MaxTokens < 0 {
		writeNativeError(c, native.OpenAIEdits, http.StatusBadRequest, errors.New("model, instruction are required and max_tokens must not be negative"))
		return
	}
	forwardNative(c, nativeInput{protocol: native.OpenAIEdits, model: fields.Model,
		stream: fields.Stream, maxOutputTokens: fields.MaxTokens, body: body})
}

// NativeOpenAIImages forwards image generation with operation-specific
// billing: the charge is the model and size ratio times the image count,
// reserved before the upstream request and charged exactly on success.
func NativeOpenAIImages(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIImages, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model   string `json:"model"`
		Prompt  string `json:"prompt"`
		N       int    `json:"n"`
		Size    string `json:"size"`
		Quality string `json:"quality"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		writeNativeError(c, native.OpenAIImages, http.StatusBadRequest, errors.New("invalid image request"))
		return
	}
	if fields.Model == "" {
		fields.Model = "dall-e-2"
	}
	if fields.N == 0 {
		fields.N = 1
	}
	if fields.Size == "" {
		fields.Size = "1024x1024"
	}
	if fields.Prompt == "" {
		writeNativeError(c, native.OpenAIImages, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}
	metadata := ginmeta.Get(c)
	mappedModel := fields.Model
	if replacement := metadata.ModelMapping[fields.Model]; replacement != "" {
		mappedModel = replacement
	}
	if !imageSizeSupported(mappedModel, fields.Size) {
		writeNativeError(c, native.OpenAIImages, http.StatusBadRequest, errors.New("size not supported for this image model"))
		return
	}
	if !imagePromptLengthSupported(mappedModel, len(fields.Prompt)) {
		writeNativeError(c, native.OpenAIImages, http.StatusBadRequest, errors.New("prompt is too long"))
		return
	}
	if !imageCountSupported(mappedModel, fields.N) {
		writeNativeError(c, native.OpenAIImages, http.StatusBadRequest, errors.New("invalid value of n"))
		return
	}
	modelRatio := billingratio.GetModelRatio(mappedModel, metadata.ChannelType)
	groupRatio := billingratio.GetGroupRatio(metadata.Group)
	ratio := modelRatio * groupRatio
	quota := int64(ratio*imageCostRatio(mappedModel, fields.Size, fields.Quality)*1000) * int64(fields.N)
	forwardNative(c, nativeInput{protocol: native.OpenAIImages, model: fields.Model, fixedQuota: quota, body: body})
}

func imageSizeSupported(model, size string) bool {
	if billingratio.ImageSizeRatios[model] == nil {
		return true
	}
	_, ok := billingratio.ImageSizeRatios[model][size]
	return ok
}

func imagePromptLengthSupported(model string, promptLength int) bool {
	maxPromptLength, ok := billingratio.ImagePromptLengthLimitations[model]
	return !ok || promptLength <= maxPromptLength
}

func imageCountSupported(model string, count int) bool {
	amounts, ok := billingratio.ImageGenerationAmounts[model]
	return !ok || (count >= amounts[0] && count <= amounts[1])
}

func imageCostRatio(model, size, quality string) float64 {
	ratio := 1.0
	if sizes, ok := billingratio.ImageSizeRatios[model]; ok {
		if value, ok := sizes[size]; ok {
			ratio = value
		}
	}
	if quality == "hd" && model == "dall-e-3" {
		if size == "1024x1024" {
			ratio *= 2
		} else {
			ratio *= 1.5
		}
	}
	return ratio
}

// NativeOpenAIAudioSpeech forwards text-to-speech with the legacy billing
// semantics: the charge is the input length times the model and group ratio,
// reported as the consume log's PromptTokens.
func NativeOpenAIAudioSpeech(c *gin.Context) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, native.OpenAIAudioSpeech, http.StatusBadRequest, err)
		return
	}
	var fields struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || fields.Model == "" {
		writeNativeError(c, native.OpenAIAudioSpeech, http.StatusBadRequest, errors.New("model is required"))
		return
	}
	if fields.Input == "" {
		writeNativeError(c, native.OpenAIAudioSpeech, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	if len(fields.Input) > 4096 {
		writeNativeError(c, native.OpenAIAudioSpeech, http.StatusBadRequest, errors.New("input is too long (over 4096 characters)"))
		return
	}
	metadata := ginmeta.Get(c)
	mappedModel := mappedModelOf(metadata, fields.Model)
	ratio := billingratio.GetModelRatio(mappedModel, metadata.ChannelType) * billingratio.GetGroupRatio(metadata.Group)
	forwardNative(c, nativeInput{protocol: native.OpenAIAudioSpeech, model: fields.Model,
		fixedQuota: int64(float64(len(fields.Input)) * ratio), fixedPromptTokens: true, body: body})
}

// NativeOpenAIAudioTranscriptions forwards a multipart transcription request
// byte-for-byte and charges the token count of the transcript text, matching
// the legacy billing for providers that omit usage.
func NativeOpenAIAudioTranscriptions(c *gin.Context) {
	nativeOpenAIAudio(c, native.OpenAIAudioTranscriptions)
}

// NativeOpenAIAudioTranslations forwards a multipart translation request with
// the same billing as transcription.
func NativeOpenAIAudioTranslations(c *gin.Context) {
	nativeOpenAIAudio(c, native.OpenAIAudioTranslations)
}

func nativeOpenAIAudio(c *gin.Context, protocol native.Protocol) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		writeNativeError(c, protocol, http.StatusBadRequest, err)
		return
	}
	model := c.GetString(ctxkey.RequestModel)
	if model == "" {
		writeNativeError(c, protocol, http.StatusBadRequest, errors.New("model is required"))
		return
	}
	responseFormat := multipartFormField(body, c.Request.Header.Get("Content-Type"), "response_format", "json")
	metadata := ginmeta.Get(c)
	mappedModel := mappedModelOf(metadata, model)
	ratio := billingratio.GetModelRatio(mappedModel, metadata.ChannelType) * billingratio.GetGroupRatio(metadata.Group)
	reserve := int64(float64(config.PreConsumedQuota) * ratio)
	forwardNative(c, nativeInput{protocol: protocol, model: model, fixedQuota: reserve,
		responseCharge: audioResponseCharge(responseFormat, mappedModel), body: body})
}

func mappedModelOf(metadata *meta.Meta, model string) string {
	if replacement := metadata.ModelMapping[model]; replacement != "" {
		return replacement
	}
	return model
}

// multipartFormField extracts one text form value without loading the file
// parts, so audio payloads are never buffered twice.
func multipartFormField(body []byte, contentType, field, fallback string) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return fallback
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			return fallback
		}
		if part.FormName() == field {
			value, err := io.ReadAll(io.LimitReader(part, 4096))
			if err != nil || len(value) == 0 {
				return fallback
			}
			return string(value)
		}
	}
}

func audioResponseCharge(responseFormat, model string) func(http.Header, []byte) (int64, int64, error) {
	return func(_ http.Header, body []byte) (int64, int64, error) {
		text, err := audioResponseText(responseFormat, body)
		if err != nil {
			return 0, 0, err
		}
		tokens := int64(openai.CountTokenText(text, model))
		return tokens, tokens, nil
	}
}

func audioResponseText(responseFormat string, body []byte) (string, error) {
	switch responseFormat {
	case "json":
		var whisper openai.WhisperJSONResponse
		if err := json.Unmarshal(body, &whisper); err != nil {
			return "", errors.New("unmarshal_response_body_failed")
		}
		return whisper.Text, nil
	case "verbose_json":
		var whisper openai.WhisperVerboseJSONResponse
		if err := json.Unmarshal(body, &whisper); err != nil {
			return "", errors.New("unmarshal_response_body_failed")
		}
		return whisper.Text, nil
	case "text":
		return strings.TrimSuffix(string(body), "\n"), nil
	case "srt", "vtt":
		return subtitleText(body), nil
	default:
		return "", errors.New("unexpected_response_format")
	}
}

// subtitleText joins the subtitle text lines that follow each timestamp.
func subtitleText(body []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	var builder strings.Builder
	var textLine bool
	for scanner.Scan() {
		line := scanner.Text()
		if textLine {
			builder.WriteString(line)
			textLine = false
			continue
		}
		if strings.Contains(line, "-->") {
			textLine = true
		}
	}
	return builder.String()
}
