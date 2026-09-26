package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/billing/ratio"
	"github.com/infinmalum/one-gateway/relay/native"
)

type NativeReservation struct {
	UserID          int
	TokenID         int
	ChannelID       int
	ChannelType     int
	TokenName       string
	ModelName       string
	Group           string
	Reserved        int64
	ModelRatio      float64
	GroupRatio      float64
	CompletionRatio float64
	StartedAt       time.Time
}

// ReserveNativeQuota uses the provider's output limit plus the configured
// input allowance as the provisional charge. The final provider usage replaces
// this charge after the response, including when a stream is interrupted.
func ReserveNativeQuota(ctx context.Context, reservation NativeReservation, maxOutputTokens int64) (*NativeReservation, error) {
	if maxOutputTokens < 0 {
		return nil, errors.New("output token limit must not be negative")
	}
	reservation.ModelRatio = ratio.GetModelRatio(reservation.ModelName, reservation.ChannelType)
	reservation.GroupRatio = ratio.GetGroupRatio(reservation.Group)
	reservation.CompletionRatio = ratio.GetCompletionRatio(reservation.ModelName, reservation.ChannelType)
	reservation.StartedAt = time.Now()
	reservation.Reserved = int64(math.Ceil(float64(config.PreConsumedQuota+maxOutputTokens) * reservation.ModelRatio * reservation.GroupRatio))
	if reservation.Reserved < 0 {
		return nil, errors.New("invalid quota reservation")
	}
	if reservation.Reserved == 0 {
		return &reservation, nil
	}
	available, err := model.CacheGetUserQuota(ctx, reservation.UserID)
	if err != nil {
		return nil, err
	}
	if available < reservation.Reserved {
		return nil, errors.New("user quota is not enough")
	}
	if err := model.PreConsumeTokenQuota(reservation.TokenID, reservation.Reserved); err != nil {
		return nil, err
	}
	if err := model.CacheDecreaseUserQuota(reservation.UserID, reservation.Reserved); err != nil {
		if refundErr := model.PostConsumeTokenQuota(reservation.TokenID, -reservation.Reserved); refundErr != nil {
			logger.Error(ctx, "failed to refund native quota reservation: "+refundErr.Error())
		}
		if refreshErr := model.CacheRefreshUserQuota(ctx, reservation.UserID); refreshErr != nil {
			logger.Error(ctx, "failed to refresh user quota cache: "+refreshErr.Error())
		}
		return nil, err
	}
	return &reservation, nil
}

func (r *NativeReservation) Refund(ctx context.Context) {
	if r == nil || r.Reserved == 0 {
		return
	}
	if err := model.PostConsumeTokenQuota(r.TokenID, -r.Reserved); err != nil {
		logger.Error(ctx, "failed to refund native quota reservation: "+err.Error())
	}
	if err := model.CacheRefreshUserQuota(ctx, r.UserID); err != nil {
		logger.Error(ctx, "failed to refresh user quota cache: "+err.Error())
	}
}

// Settle retains at least the reservation for an interrupted stream because a
// partial usage report may omit tokens already generated upstream.
func (r *NativeReservation) Settle(ctx context.Context, usage native.Usage, streamed, interrupted bool) {
	if r == nil {
		return
	}
	quota := r.Reserved
	if usage.Seen {
		quota = int64(math.Ceil((float64(usage.Input) + float64(usage.Output)*r.CompletionRatio) * r.ModelRatio * r.GroupRatio))
		if quota == 0 && r.ModelRatio*r.GroupRatio > 0 && usage.Input+usage.Output > 0 {
			quota = 1
		}
	}
	if interrupted {
		quota = max(quota, r.Reserved)
	}
	if err := model.PostConsumeTokenQuota(r.TokenID, quota-r.Reserved); err != nil {
		logger.Error(ctx, "failed to settle native quota: "+err.Error())
		quota = r.Reserved
	}
	if err := model.CacheRefreshUserQuota(ctx, r.UserID); err != nil {
		logger.Error(ctx, "failed to refresh user quota cache: "+err.Error())
	}
	if quota == 0 {
		return
	}
	content := fmt.Sprintf("native protocol; ratio: %.2f × %.2f × %.2f", r.ModelRatio, r.GroupRatio, r.CompletionRatio)
	if !usage.Seen {
		content += "; upstream usage unavailable, reserved quota retained"
	}
	if interrupted {
		content += "; stream interrupted"
	}
	model.RecordConsumeLog(ctx, &model.Log{
		UserId:           r.UserID,
		ChannelId:        r.ChannelID,
		PromptTokens:     int(usage.Input),
		CompletionTokens: int(usage.Output),
		ModelName:        r.ModelName,
		TokenName:        r.TokenName,
		Quota:            int(quota),
		Content:          content,
		IsStream:         streamed,
		ElapsedTime:      time.Since(r.StartedAt).Milliseconds(),
	})
	model.UpdateUserUsedQuotaAndRequestCount(r.UserID, quota)
	model.UpdateChannelUsedQuota(r.ChannelID, quota)
}
