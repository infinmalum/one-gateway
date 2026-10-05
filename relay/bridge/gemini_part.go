package bridge

import (
	"encoding/json"
	"errors"
)

type geminiPartKind int

const (
	geminiPartText geminiPartKind = iota
	geminiPartThought
	geminiPartFunctionCall
	geminiPartSignature
)

// geminiPart is the converted view of one Gemini candidate content part.
// Only parts with a representation in Chat Completions or Anthropic Messages
// parse; everything else fails so no content is silently dropped.
type geminiPart struct {
	kind      geminiPartKind
	text      string
	signature string
	callID    string
	name      string
	args      json.RawMessage
}

// parseGeminiResponsePart parses one candidate content part. Thought parts
// may carry the thought signature that must be echoed back on later requests.
// A bare signature part attaches to the preceding thinking block. Function
// calls and display text carrying a thought signature have no Chat or
// Messages slot and are rejected.
func parseGeminiResponsePart(raw json.RawMessage) (geminiPart, error) {
	fields, err := object(raw, "text", "thought", "thoughtSignature", "functionCall")
	if err != nil {
		return geminiPart{}, err
	}
	if len(fields["functionCall"]) != 0 {
		if len(fields["text"]) != 0 || len(fields["thought"]) != 0 {
			return geminiPart{}, errors.New("Gemini part combines a function call with other content")
		}
		if len(fields["thoughtSignature"]) != 0 {
			return geminiPart{}, errors.New("Gemini function call carries a thought signature that Chat and Messages cannot represent")
		}
		call, err := object(fields["functionCall"], "name", "args", "id")
		if err != nil {
			return geminiPart{}, err
		}
		name := rawString(call["name"])
		if name == "" {
			return geminiPart{}, errors.New("Gemini function call requires a name")
		}
		args := json.RawMessage("{}")
		if len(call["args"]) != 0 && string(call["args"]) != "null" {
			var input map[string]any
			if json.Unmarshal(call["args"], &input) != nil || input == nil {
				return geminiPart{}, errors.New("Gemini function call args must be an object")
			}
			args = call["args"]
		}
		return geminiPart{kind: geminiPartFunctionCall, name: name, args: args, callID: rawString(call["id"])}, nil
	}
	var thought bool
	if len(fields["thought"]) != 0 {
		if json.Unmarshal(fields["thought"], &thought) != nil {
			return geminiPart{}, errors.New("Gemini thought flag must be boolean")
		}
	}
	signature := rawString(fields["thoughtSignature"])
	if thought {
		if rawString(fields["text"]) == "" && signature == "" {
			return geminiPart{}, errors.New("Gemini thought part requires text or a signature")
		}
		return geminiPart{kind: geminiPartThought, text: rawString(fields["text"]), signature: signature}, nil
	}
	if len(fields["text"]) != 0 {
		if signature != "" {
			return geminiPart{}, errors.New("Gemini display text carries a thought signature that Chat and Messages cannot represent")
		}
		return geminiPart{kind: geminiPartText, text: rawString(fields["text"])}, nil
	}
	if signature != "" {
		return geminiPart{kind: geminiPartSignature, signature: signature}, nil
	}
	return geminiPart{}, errors.New("Gemini part cannot be represented")
}
