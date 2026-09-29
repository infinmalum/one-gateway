package lifecycle

import (
	"net/http"

	"github.com/infinmalum/one-gateway/model"
)

// Selection describes channel requirements independently of the client
// protocol and the HTTP framework. Type < 0 accepts any enabled channel.
type Selection struct {
	Group          string
	Model          string
	Type           int
	SpecificID     int
	IgnorePriority bool
	Excluded       map[int]bool
}

func SelectChannel(input Selection) (*model.Channel, *HTTPError) {
	if input.SpecificID != 0 {
		if input.SpecificID < 0 {
			return nil, &HTTPError{Status: http.StatusBadRequest, Message: "invalid channel ID"}
		}
		channel, err := model.GetChannelById(input.SpecificID, true)
		if err != nil || channel == nil {
			return nil, &HTTPError{Status: http.StatusBadRequest, Message: "invalid channel ID"}
		}
		if channel.Status != model.ChannelStatusEnabled {
			return nil, &HTTPError{Status: http.StatusForbidden, Message: "selected channel is disabled"}
		}
		if input.Type >= 0 && channel.Type != input.Type {
			return nil, &HTTPError{Status: http.StatusBadRequest, Message: "selected channel does not support this protocol"}
		}
		return channel, nil
	}
	var channel *model.Channel
	var err error
	if input.Type >= 0 {
		channel, err = model.GetRandomSatisfiedChannelByTypeExcluding(input.Group, input.Model, input.Type, input.IgnorePriority, input.Excluded)
	} else if len(input.Excluded) == 0 {
		channel, err = model.CacheGetRandomSatisfiedChannel(input.Group, input.Model, input.IgnorePriority)
	} else {
		channel, err = model.GetRandomSatisfiedChannelExcluding(input.Group, input.Model, input.IgnorePriority, input.Excluded)
	}
	if err != nil || channel == nil {
		return nil, &HTTPError{Status: http.StatusServiceUnavailable, Message: "no compatible channel for this model"}
	}
	return channel, nil
}

// Retryable applies equally to native and legacy forwarding. A pinned
// channel, cancelled request, or response that has started cannot be retried.
func Retryable(status int, outputStarted, pinned, cancelled bool) bool {
	if outputStarted || pinned || cancelled {
		return false
	}
	return status == http.StatusTooManyRequests || status >= 500 && status <= 599
}
