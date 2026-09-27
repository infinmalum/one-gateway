package lifecycle

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/billing"
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
	UserID          int
	TokenID         int
	TokenName       string
	Group           string
	SpecificChannel bool
}

type Request struct {
	Protocol        native.Protocol
	Model           string
	Action          string
	Version         string
	Stream          bool
	MaxOutputTokens int64
	Body            []byte
	Headers         http.Header
	Query           url.Values
	Channel         Channel
	Principal       Principal
	RetryLimit      int
	HTTPClient      *http.Client
}

type HTTPError struct {
	Status  int
	Message string
}

// Forward executes same-protocol HTTP requests. It owns reservation, retries,
// response copying, and settlement; the caller owns request parsing and the
// client protocol's error envelope.
func Forward(ctx context.Context, dst http.ResponseWriter, input Request) *HTTPError {
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
		req, err := native.BuildRequest(ctx, native.Request{
			Protocol: input.Protocol, BaseURL: channel.BaseURL, Version: version,
			Model: mappedModel, Action: input.Action, APIKey: channel.APIKey,
			SystemPrompt: channel.SystemPrompt, Body: input.Body,
			Headers: input.Headers, Query: input.Query,
		})
		if err != nil {
			return &HTTPError{http.StatusBadRequest, err.Error()}
		}
		reservation, err := billing.ReserveNativeQuota(ctx, billing.NativeReservation{
			UserID: input.Principal.UserID, TokenID: input.Principal.TokenID,
			ChannelID: channel.ID, ChannelType: channel.Type,
			TokenName: input.Principal.TokenName, ModelName: mappedModel,
			Group: input.Principal.Group, SystemPromptReset: channel.SystemPrompt != "",
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
		copyHeaders(dst.Header(), response.Header)
		dst.WriteHeader(response.StatusCode)
		usage, copyErr := native.CopyResponse(dst, response.Body, input.Protocol, input.Stream)
		_ = response.Body.Close()
		interrupted := copyErr != nil || ctx.Err() != nil || (input.Stream && !usage.Complete)
		reservation.Settle(settlementContext, usage, input.Stream, interrupted)
		if copyErr != nil {
			logger.Errorf(settlementContext, "upstream response interrupted on channel %d: %v", channel.ID, copyErr)
		}
		return nil
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
