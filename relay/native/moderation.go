package native

import (
	"encoding/json"
)

const DefaultModerationModel = "omni-moderation-latest"

// EstimateModerationInputTokens supplies billing usage when the provider omits
// it. Image inputs are deliberately left unestimated so the reservation is
// retained rather than pretending image bytes are text tokens.
func EstimateModerationInputTokens(raw json.RawMessage) int64 {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return estimateTextTokens(text)
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return 0
	}
	var total int64
	for _, item := range items {
		if json.Unmarshal(item, &text) == nil {
			total += estimateTextTokens(text)
			continue
		}
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &part) != nil || part.Type != "text" {
			return 0
		}
		total += estimateTextTokens(part.Text)
	}
	return total
}

func estimateTextTokens(text string) int64 {
	if text == "" {
		return 0
	}
	return int64((len(text) + 3) / 4)
}
