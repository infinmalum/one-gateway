package lifecycle

import (
	"context"
	"net/http"

	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/billing"
	"github.com/infinmalum/one-gateway/relay/native"
)

// AdapterResult describes one provider-specific attempt. Legacy provider
// adapters own their wire conversion, while the shared lifecycle owns channel
// retry and quota settlement.
type AdapterResult struct {
	Usage         native.Usage
	OutputStarted bool
	Err           *HTTPError
}

type AdapterAttempt func(channel Channel, mappedModel string) AdapterResult

// ForwardAdapter runs provider-specific transports under the same retry and
// once-only reservation rules as native forwarding. An attempt may retry only
// if it failed before writing to the client. Fixed-quota operations (provider
// image formats) reserve and settle their exact amount per attempt.
func ForwardAdapter(ctx context.Context, input Request, attempt AdapterAttempt) *HTTPError {
	if input.Channel.ID == 0 {
		return &HTTPError{Status: http.StatusBadRequest, Message: "channel was not selected"}
	}
	if input.Principal.Group == "" {
		group, err := model.CacheGetUserGroup(input.Principal.UserID)
		if err != nil {
			return &HTTPError{Status: http.StatusInternalServerError, Message: "failed to load user group"}
		}
		input.Principal.Group = group
	}
	settlementContext := context.WithoutCancel(ctx)
	remaining := max(0, input.RetryLimit)
	tried := make(map[int]bool)
	channel := input.Channel
	for {
		tried[channel.ID] = true
		mappedModel := input.Model
		if replacement := channel.ModelMapping[input.Model]; replacement != "" {
			mappedModel = replacement
		}
		payload := billing.NativeReservation{
			UserID: input.Principal.UserID, TokenID: input.Principal.TokenID,
			ChannelID: channel.ID, ChannelType: channel.Type,
			TokenName: input.Principal.TokenName, ModelName: mappedModel,
			Group: input.Principal.Group, SystemPromptReset: channel.SystemPrompt != "",
		}
		var reservation *billing.NativeReservation
		var err error
		if input.FixedQuota > 0 {
			reservation, err = billing.ReserveFixedQuota(ctx, payload, input.FixedQuota)
		} else {
			reservation, err = billing.ReserveNativeQuota(ctx, payload, input.MaxOutputTokens)
		}
		if err != nil {
			return &HTTPError{Status: http.StatusForbidden, Message: err.Error()}
		}
		result := attempt(channel, mappedModel)
		if result.Err != nil {
			if result.OutputStarted {
				if input.FixedQuota > 0 {
					reservation.SettleDirect(settlementContext, input.FixedQuota, 0, true)
				} else {
					reservation.Settle(settlementContext, result.Usage, input.Stream, true)
				}
				logger.Errorf(settlementContext, "provider response interrupted on channel %d: %s", channel.ID, result.Err.Message)
				return nil
			}
			reservation.Refund(settlementContext)
			if Retryable(result.Err.Status, false, input.Principal.SpecificChannel, ctx.Err() != nil) {
				if next, ok := selectRetry(ctx, input, channel, tried, &remaining); ok {
					channel = next
					continue
				}
			}
			return result.Err
		}
		if input.FixedQuota > 0 {
			reservation.SettleDirect(settlementContext, input.FixedQuota, 0, ctx.Err() != nil)
		} else {
			reservation.Settle(settlementContext, result.Usage, input.Stream,
				ctx.Err() != nil || (input.Stream && !result.Usage.Complete))
		}
		return nil
	}
}
