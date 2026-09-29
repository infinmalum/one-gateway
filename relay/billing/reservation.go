package billing

import (
	"context"
	"errors"
	"sync"

	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/model"
)

// Reservation is the protocol-neutral quota transaction for one upstream
// attempt. Retry attempts create their own reservation after the previous one
// has been refunded. A reservation can be settled or refunded exactly once.
type Reservation struct {
	UserID   int
	TokenID  int
	Reserved int64
	mu       sync.Mutex
	finished bool
	charged  int64
}

func Reserve(ctx context.Context, userID, tokenID int, amount int64) (*Reservation, error) {
	if amount < 0 {
		return nil, errors.New("quota reservation must not be negative")
	}
	r := &Reservation{UserID: userID, TokenID: tokenID, Reserved: amount}
	if amount == 0 {
		return r, nil
	}
	available, err := model.CacheGetUserQuota(ctx, userID)
	if err != nil {
		return nil, err
	}
	if available < amount {
		return nil, errors.New("user quota is not enough")
	}
	if err := model.PreConsumeTokenQuota(tokenID, amount); err != nil {
		return nil, err
	}
	if err := model.CacheDecreaseUserQuota(userID, amount); err != nil {
		if refundErr := model.PostConsumeTokenQuota(tokenID, -amount); refundErr != nil {
			logger.Error(ctx, "failed to refund quota reservation: "+refundErr.Error())
		}
		if refreshErr := model.CacheRefreshUserQuota(context.WithoutCancel(ctx), userID); refreshErr != nil {
			logger.Error(ctx, "failed to refresh user quota after refund: "+refreshErr.Error())
		}
		return nil, err
	}
	return r, nil
}

func (r *Reservation) Refund(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	if r.Reserved != 0 {
		if err := model.PostConsumeTokenQuota(r.TokenID, -r.Reserved); err != nil {
			logger.Error(ctx, "failed to refund quota reservation: "+err.Error())
			return
		}
		if err := model.CacheRefreshUserQuota(ctx, r.UserID); err != nil {
			logger.Error(ctx, "failed to refresh user quota after refund: "+err.Error())
		}
	}
	r.finished = true
}

// Settle returns the amount actually charged. On a database error the
// reservation remains charged, and the caller can log that amount accurately.
func (r *Reservation) Settle(ctx context.Context, amount int64) int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return r.charged
	}
	if amount < 0 {
		amount = r.Reserved
	}
	if delta := amount - r.Reserved; delta != 0 {
		if err := model.PostConsumeTokenQuota(r.TokenID, delta); err != nil {
			logger.Error(ctx, "failed to settle quota: "+err.Error())
			amount = r.Reserved
		}
	}
	if err := model.CacheRefreshUserQuota(ctx, r.UserID); err != nil {
		logger.Error(ctx, "failed to refresh user quota after settlement: "+err.Error())
	}
	r.finished = true
	r.charged = amount
	return amount
}
