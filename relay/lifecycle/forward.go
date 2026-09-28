package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/billing"
	"github.com/infinmalum/one-gateway/relay/bridge"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"github.com/infinmalum/one-gateway/relay/native"
)

// Channel contains only upstream transport settings. Account and token data
// remain separate so a retry can replace the channel without changing the user.
type Channel struct {
	ID           int
	Type         int
	BaseURL      string
	APIKey       string
	ModelMapping map[string]string
	SystemPrompt string
	APIVersion   string
}

type Principal struct {
	UserID            int
	TokenID           int
	TokenName         string
	Group             string
	SpecificChannel   bool
	SpecificChannelID int
}

type Request struct {
	Protocol         native.Protocol
	UpstreamProtocol native.Protocol
	// FallbackUpstreamProtocols is tried in order when no channel for the
	// primary upstream protocol can serve the model.
	FallbackUpstreamProtocols []native.Protocol
	Model                     string
	Action                    string
	Version                   string
	Stream                    bool
	MaxOutputTokens           int64
	// FallbackInputTokens is used by operations whose successful response may
	// omit usage, such as Moderations. A provider usage report takes precedence.
	FallbackInputTokens int64
	Body                []byte
	Headers             http.Header
	Query               url.Values
	Channel             Channel
	Principal           Principal
	RetryLimit          int
	HTTPClient          *http.Client
}

type HTTPError struct {
	Status  int
	Message string
}

// Forward executes native passthrough and explicit JSON protocol conversions.
// It owns reservation, retries, response copying, and settlement; the caller
// owns request parsing and the client protocol's error envelope.
func Forward(ctx context.Context, dst http.ResponseWriter, input Request) *HTTPError {
	upstreamProtocol := input.Protocol
	if input.UpstreamProtocol != "" {
		upstreamProtocol = input.UpstreamProtocol
	}
	if input.Principal.Group == "" {
		group, err := model.CacheGetUserGroup(input.Principal.UserID)
		if err != nil {
			return &HTTPError{http.StatusInternalServerError, "failed to load user group"}
		}
		input.Principal.Group = group
	}
	if input.Channel.ID == 0 {
		var primaryErr *HTTPError
		var selected Channel
		candidates := append([]native.Protocol{upstreamProtocol}, input.FallbackUpstreamProtocols...)
		for _, candidate := range candidates {
			candidateChannel, err := selectInitial(input, candidate)
			if err == nil {
				selected = candidateChannel
				upstreamProtocol = candidate
				break
			}
			if primaryErr == nil {
				primaryErr = err
				if err.Status != http.StatusServiceUnavailable && !(input.Principal.SpecificChannel && err.Status == http.StatusBadRequest) {
					return err
				}
			}
		}
		if selected.ID == 0 {
			return primaryErr
		}
		input.Channel = selected
	} else if input.Channel.Type != requiredChannelType(upstreamProtocol) {
		matched := false
		for _, candidate := range input.FallbackUpstreamProtocols {
			if input.Channel.Type == requiredChannelType(candidate) {
				upstreamProtocol = candidate
				matched = true
				break
			}
		}
		if !matched {
			return &HTTPError{http.StatusBadRequest, "selected channel does not support this protocol"}
		}
	}
	converter, err := bridge.For(input.Protocol, upstreamProtocol)
	if err != nil {
		return &HTTPError{http.StatusUnprocessableEntity, err.Error()}
	}
	var streamConverter bridge.StreamConverter
	if converter != nil && input.Stream {
		candidate, ok := converter.(bridge.StreamConverter)
		if !ok {
			return &HTTPError{http.StatusUnprocessableEntity, "streaming protocol conversion is not supported"}
		}
		streamConverter = candidate
	}
	httpClient := input.HTTPClient
	if httpClient == nil {
		httpClient = client.HTTPClient
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	providerClient := *httpClient
	providerClient.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 3 || next.URL.Scheme != previous[0].URL.Scheme || !strings.EqualFold(next.URL.Host, previous[0].URL.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	settlementContext := context.WithoutCancel(ctx)
	retries := max(0, input.RetryLimit)
	tried := make(map[int]bool)
	channel := input.Channel
	// The Gemini action is selected by the transport, so a fallback onto a
	// Gemini channel derives it from the final upstream protocol instead of
	// requiring the controller to know the routing outcome.
	action := input.Action
	if upstreamProtocol == native.Gemini {
		action = "generateContent"
		if input.Stream {
			action = "streamGenerateContent"
		}
	}
	for {
		tried[channel.ID] = true
		mappedModel := input.Model
		if replacement := channel.ModelMapping[input.Model]; replacement != "" {
			mappedModel = replacement
		}
		version := input.Version
		if channel.APIVersion != "" {
			version = channel.APIVersion
		}
		body := input.Body
		if converter != nil {
			var err error
			body, err = converter.Request(body, mappedModel)
			if err != nil {
				return &HTTPError{http.StatusUnprocessableEntity, err.Error()}
			}
		}
		req, err := native.BuildRequest(ctx, native.Request{
			Protocol: upstreamProtocol, BaseURL: channel.BaseURL, Version: version,
			Model: mappedModel, Action: action, APIKey: channel.APIKey,
			SystemPrompt: channel.SystemPrompt, Body: body,
			Headers: input.Headers, Query: input.Query,
		})
		if err != nil {
			return &HTTPError{http.StatusBadRequest, err.Error()}
		}
		reservation, err := billing.ReserveNativeQuota(ctx, billing.NativeReservation{
			UserID: input.Principal.UserID, TokenID: input.Principal.TokenID,
			ChannelID: channel.ID, ChannelType: channel.Type,
			TokenName: input.Principal.TokenName, ModelName: mappedModel,
			Group: input.Principal.Group, SystemPromptReset: channel.SystemPrompt != "" && supportsSystemPrompt(upstreamProtocol),
		}, input.MaxOutputTokens)
		if err != nil {
			return &HTTPError{http.StatusForbidden, err.Error()}
		}
		response, err := providerClient.Do(req)
		if err != nil {
			reservation.Refund(settlementContext)
			logger.Errorf(settlementContext, "upstream request failed on channel %d: %v", channel.ID, err)
			if next, ok := selectRetry(ctx, input, channel, tried, &retries); ok {
				channel = next
				continue
			}
			return &HTTPError{http.StatusBadGateway, "upstream request failed"}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			reservation.Refund(settlementContext)
			if response.StatusCode >= 300 && response.StatusCode < 400 {
				_ = response.Body.Close()
				return &HTTPError{http.StatusBadGateway, "upstream redirect is not allowed"}
			}
			if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
				if next, ok := selectRetry(ctx, input, channel, tried, &retries); ok {
					_ = response.Body.Close()
					channel = next
					continue
				}
			}
			if converter != nil {
				message := convertedUpstreamError(response.Body)
				_ = response.Body.Close()
				copyHeaders(dst.Header(), response.Header)
				return &HTTPError{response.StatusCode, message}
			}
			copyHeaders(dst.Header(), response.Header)
			dst.WriteHeader(response.StatusCode)
			_, _ = io.Copy(dst, response.Body)
			_ = response.Body.Close()
			return nil
		}
		if input.Stream && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			reservation.Refund(settlementContext)
			_ = response.Body.Close()
			return &HTTPError{http.StatusBadGateway, "upstream did not return an event stream"}
		}
		if streamConverter != nil {
			copyHeaders(dst.Header(), response.Header)
			dst.Header().Set("Content-Type", "text/event-stream")
			dst.Header().Del("Content-Length")
			dst.WriteHeader(http.StatusOK)
			usage, streamErr := streamConverter.Stream(flushingWriter{dst}, response.Body, input.Model)
			_ = response.Body.Close()
			interrupted := streamErr != nil || ctx.Err() != nil || !usage.Complete
			reservation.Settle(settlementContext, usage, true, interrupted)
			if streamErr != nil {
				logger.Errorf(settlementContext, "converted stream interrupted on channel %d: %v", channel.ID, streamErr)
			}
			return nil
		}
		if converter != nil {
			const maxConvertedResponse = 16 << 20
			body, readErr := io.ReadAll(io.LimitReader(response.Body, maxConvertedResponse+1))
			_ = response.Body.Close()
			if readErr != nil || len(body) > maxConvertedResponse {
				reservation.Settle(settlementContext, native.Usage{}, false, true)
				return &HTTPError{http.StatusBadGateway, "upstream response could not be read"}
			}
			converted, usage, convertErr := converter.Response(body, input.Model)
			if convertErr != nil {
				reservation.Settle(settlementContext, native.Usage{}, false, true)
				return &HTTPError{http.StatusBadGateway, convertErr.Error()}
			}
			copyHeaders(dst.Header(), response.Header)
			dst.Header().Set("Content-Type", "application/json")
			dst.Header().Del("Content-Length")
			dst.WriteHeader(http.StatusOK)
			_, writeErr := dst.Write(converted)
			reservation.Settle(settlementContext, usage, false, writeErr != nil || ctx.Err() != nil)
			if writeErr != nil {
				logger.Errorf(settlementContext, "converted response interrupted on channel %d: %v", channel.ID, writeErr)
			}
			return nil
		}
		copyHeaders(dst.Header(), response.Header)
		dst.WriteHeader(response.StatusCode)
		usage, copyErr := native.CopyResponse(dst, response.Body, input.Protocol, input.Stream)
		_ = response.Body.Close()
		if copyErr == nil && usage.Complete && !usage.Seen && input.FallbackInputTokens > 0 {
			usage.Input = input.FallbackInputTokens
			usage.Seen = true
		}
		interrupted := copyErr != nil || ctx.Err() != nil || (input.Stream && !usage.Complete)
		reservation.Settle(settlementContext, usage, input.Stream, interrupted)
		if copyErr != nil {
			logger.Errorf(settlementContext, "upstream response interrupted on channel %d: %v", channel.ID, copyErr)
		}
		return nil
	}
}

func supportsSystemPrompt(protocol native.Protocol) bool {
	switch protocol {
	case native.Anthropic, native.Gemini, native.OpenAIChat, native.OpenAIResponses:
		return true
	default:
		return false
	}
}

// flushingWriter lets bridge converters stream increments through the Response
// Writer without depending on Gin.
type flushingWriter struct {
	writer http.ResponseWriter
}

func (f flushingWriter) Write(p []byte) (int, error) {
	return f.writer.Write(p)
}

func (f flushingWriter) Flush() {
	if flusher, ok := f.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func selectInitial(input Request, upstreamProtocol native.Protocol) (Channel, *HTTPError) {
	requiredType := requiredChannelType(upstreamProtocol)
	if requiredType < 0 {
		return Channel{}, &HTTPError{http.StatusBadRequest, "unsupported native protocol"}
	}
	var selected *model.Channel
	var err error
	if input.Principal.SpecificChannel {
		if input.Principal.SpecificChannelID <= 0 {
			return Channel{}, &HTTPError{http.StatusBadRequest, "invalid channel ID"}
		}
		selected, err = model.GetChannelById(input.Principal.SpecificChannelID, true)
		if err != nil || selected == nil {
			return Channel{}, &HTTPError{http.StatusBadRequest, "invalid channel ID"}
		}
		if selected.Status != model.ChannelStatusEnabled {
			return Channel{}, &HTTPError{http.StatusForbidden, "selected channel is disabled"}
		}
		if selected.Type != requiredType {
			return Channel{}, &HTTPError{http.StatusBadRequest, "selected channel does not support this protocol"}
		}
	} else {
		selected, err = model.GetRandomSatisfiedChannelByType(input.Principal.Group, input.Model, requiredType, false)
		if err != nil || selected == nil {
			label := "OpenAI"
			switch upstreamProtocol {
			case native.Anthropic:
				label = "Anthropic"
			case native.Gemini:
				label = "Gemini"
			}
			return Channel{}, &HTTPError{http.StatusServiceUnavailable, "no compatible " + label + " channel for this model"}
		}
	}
	channel, err := channelFromModel(selected)
	if err != nil {
		return Channel{}, &HTTPError{http.StatusInternalServerError, "failed to load channel configuration"}
	}
	return channel, nil
}

func convertedUpstreamError(body io.Reader) string {
	const maxError = 64 << 10
	data, _ := io.ReadAll(io.LimitReader(body, maxError))
	var result struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &result) == nil && result.Error.Message != "" {
		return result.Error.Message
	}
	return "upstream request failed"
}

func requiredChannelType(protocol native.Protocol) int {
	switch protocol {
	case native.Anthropic:
		return channeltype.Anthropic
	case native.Gemini:
		return channeltype.Gemini
	case native.OpenAIChat, native.OpenAICompletions, native.OpenAIEmbeddings, native.OpenAIModerations, native.OpenAIResponses:
		return channeltype.OpenAI
	default:
		return -1
	}
}

func selectRetry(ctx context.Context, input Request, failed Channel, tried map[int]bool, remaining *int) (Channel, bool) {
	if *remaining <= 0 || ctx.Err() != nil || input.Principal.SpecificChannel {
		return Channel{}, false
	}
	selected, err := model.GetRandomSatisfiedChannelByTypeExcluding(input.Principal.Group, input.Model, failed.Type, false, tried)
	if err != nil {
		return Channel{}, false
	}
	next, err := channelFromModel(selected)
	if err != nil {
		logger.Errorf(ctx, "failed to load retry channel %d: %v", selected.Id, err)
		return Channel{}, false
	}
	*remaining = *remaining - 1
	logger.Infof(ctx, "retrying request on channel %d after channel %d", next.ID, failed.ID)
	return next, true
}

func channelFromModel(channel *model.Channel) (Channel, error) {
	if channel == nil || channel.Type < 0 || channel.Type >= len(channeltype.ChannelBaseURLs) {
		return Channel{}, errors.New("invalid channel type")
	}
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = channeltype.ChannelBaseURLs[channel.Type]
	}
	config, err := channel.LoadConfig()
	if err != nil {
		return Channel{}, err
	}
	if config.APIVersion == "" && channel.Type == channeltype.Gemini && channel.Other != nil {
		config.APIVersion = *channel.Other
	}
	selected := Channel{
		ID: channel.Id, Type: channel.Type, BaseURL: baseURL, APIKey: channel.Key,
		ModelMapping: channel.GetModelMapping(), APIVersion: config.APIVersion,
	}
	if channel.SystemPrompt != nil {
		selected.SystemPrompt = *channel.SystemPrompt
	}
	return selected, nil
}

func copyHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Request-Id", "X-Request-Id", "Retry-After", "Anthropic-Version", "X-Goog-Request-Id", "OpenAI-Processing-Ms", "OpenAI-Version"} {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
}
