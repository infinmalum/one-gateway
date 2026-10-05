package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	relaycontroller "github.com/infinmalum/one-gateway/relay/controller"
	"github.com/infinmalum/one-gateway/relay/lifecycle"
	"github.com/infinmalum/one-gateway/relay/model"
	"github.com/infinmalum/one-gateway/relay/native"
	"github.com/infinmalum/one-gateway/relay/relaymode"
)

// openAIWireChannel reports whether the channel selected by the dispatcher
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
		result := relaycontroller.RelayProviderChat(c)
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
	writeProviderError(c, relaycontroller.RelayProviderText(c, relaymode.Completions))
}

// RelayEmbeddings serves OpenAI wire channels natively; provider channels with
// their own embeddings formats keep their adapter conversions.
func RelayEmbeddings(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAIEmbeddings(c)
		return
	}
	writeProviderError(c, relaycontroller.RelayProviderText(c, relaymode.Embeddings))
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
// image-specific billing; provider image formats (Zhipu, Ali, Replicate,
// Baidu) keep their adapter conversions.
func RelayImages(c *gin.Context) {
	if openAIWireChannel(c) {
		NativeOpenAIImages(c)
		return
	}
	writeProviderError(c, relaycontroller.RelayProviderImages(c))
}

// writeProviderError presents a provider-route failure in the OpenAI error
// envelope, preserving upstream details when the adapter reported them.
func writeProviderError(c *gin.Context, result *lifecycle.HTTPError) {
	if result == nil {
		return
	}
	if c.Writer.Written() {
		return
	}
	if result.Upstream != nil {
		c.JSON(result.Status, native.ErrorBodyWithUpstream(native.OpenAIChat, result.Status, result.Message,
			result.Upstream.Type, result.Upstream.Code, result.Upstream.Param))
		return
	}
	c.JSON(result.Status, native.ErrorBody(native.OpenAIChat, result.Status, result.Message))
}

// RelayAudio serves the three audio operations on OpenAI wire channels,
// including their Azure deployment variants, and rejects provider channels:
// the audio routes only ever spoke the OpenAI wire format.
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
