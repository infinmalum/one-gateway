package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/infinmalum/one-gateway/relay/native"
)

// geminiStreamEvent holds the converted view of one streaming
// GenerateContentResponse. Non-text parts and unknown candidate fields are
// rejected by the parser so neither stream direction can drop them.
type geminiStreamEvent struct {
	textParts    []string
	finishReason string
	responseID   string
	metadata     map[string]json.RawMessage
	prompt       int64
	output       int64
	cached       int64
	hasUsage     bool
}

func parseGeminiStreamEvent(data []byte) (*geminiStreamEvent, error) {
	fields, extra, err := rawObject(data, "candidates", "promptFeedback", "usageMetadata", "modelVersion", "responseId", "modelStatus")
	if err != nil {
		return nil, fmt.Errorf("invalid Gemini event: %w", err)
	}
	event := &geminiStreamEvent{metadata: map[string]json.RawMessage{}}
	for name, value := range extra {
		event.metadata[name] = value
	}
	event.responseID = rawString(fields["responseId"])
	for _, name := range []string{"promptFeedback", "modelVersion", "modelStatus"} {
		if len(fields[name]) != 0 {
			event.metadata[name] = fields[name]
		}
	}
	if len(fields["usageMetadata"]) != 0 {
		prompt, output, cached, err := geminiUsage(fields["usageMetadata"], false)
		if err != nil {
			return nil, err
		}
		event.prompt, event.output, event.cached = prompt, output, cached
		event.hasUsage = true
		event.metadata["usageMetadata"] = fields["usageMetadata"]
	}
	if len(fields["candidates"]) == 0 || string(fields["candidates"]) == "null" {
		return event, nil
	}
	var rawCandidates []json.RawMessage
	if json.Unmarshal(fields["candidates"], &rawCandidates) != nil || len(rawCandidates) > 1 {
		return nil, errors.New("Gemini event must contain at most one candidate")
	}
	if len(rawCandidates) == 0 {
		return event, nil
	}
	candidate, extra, err := rawObject(rawCandidates[0], "content", "finishReason", "safetyRatings", "citationMetadata", "tokenCount", "groundingAttributions", "groundingMetadata", "avgLogprobs", "logprobsResult", "urlContextMetadata", "index", "finishMessage")
	if err != nil {
		return nil, fmt.Errorf("invalid Gemini candidate: %w", err)
	}
	for key, value := range extra {
		event.metadata[key] = value
	}
	for _, name := range []string{"safetyRatings", "citationMetadata", "tokenCount", "groundingAttributions", "groundingMetadata", "avgLogprobs", "logprobsResult", "urlContextMetadata", "finishMessage"} {
		if len(candidate[name]) != 0 {
			event.metadata[name] = candidate[name]
		}
	}
	if len(candidate["index"]) != 0 {
		var index int
		if json.Unmarshal(candidate["index"], &index) != nil || index != 0 {
			return nil, errors.New("Gemini candidate index must be zero")
		}
	}
	if reason := rawString(candidate["finishReason"]); reason != "" {
		if _, err := mapGeminiFinishReason(reason); err != nil {
			return nil, err
		}
		event.finishReason = reason
	}
	if len(candidate["content"]) == 0 || string(candidate["content"]) == "null" {
		return event, nil
	}
	content, extra, err := rawObject(candidate["content"], "role", "parts")
	if err != nil {
		return nil, fmt.Errorf("invalid Gemini content: %w", err)
	}
	if len(extra) > 0 {
		return nil, fmt.Errorf("Gemini content field %q cannot be converted", firstKey(extra))
	}
	if role := rawString(content["role"]); role != "" && role != "model" {
		return nil, errors.New("Gemini response content role must be model")
	}
	var rawParts []json.RawMessage
	if json.Unmarshal(content["parts"], &rawParts) != nil {
		return nil, errors.New("Gemini content parts must be an array")
	}
	for _, rawPart := range rawParts {
		part, err := object(rawPart, "text")
		if err != nil || len(part) != 1 {
			return nil, errors.New("Gemini event contains a part that cannot be converted")
		}
		var value string
		if json.Unmarshal(part["text"], &value) != nil {
			return nil, errors.New("Gemini text part must contain text")
		}
		event.textParts = append(event.textParts, value)
	}
	return event, nil
}

// Stream converts Gemini GenerateContent events into Chat Completions chunks.
// The stream ends when the upstream connection closes; Gemini has no terminal
// event, so a missing finish reason fails the conversion.
func (chatGemini) Stream(dst StreamWriter, src io.Reader, requestedModel string) (native.Usage, error) {
	state := &geminiToChatStream{dst: dst, model: requestedModel, created: streamTimestamp()}
	reader := newSSEReader(src)
	for {
		data, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return state.usage, err
		}
		if err := state.event(data); err != nil {
			_ = writeStreamError(dst, err.Error())
			return state.usage, err
		}
	}
	if err := state.complete(); err != nil {
		return state.usage, err
	}
	return state.usage, nil
}

type geminiToChatStream struct {
	dst          StreamWriter
	model        string
	created      int64
	id           string
	finishReason string
	usage        native.Usage
	metadata     map[string]json.RawMessage
	events       int
}

func (s *geminiToChatStream) event(data []byte) error {
	parsed, err := parseGeminiStreamEvent(data)
	if err != nil {
		return err
	}
	s.events++
	if s.id == "" && parsed.responseID != "" {
		s.id = parsed.responseID
	}
	if parsed.hasUsage {
		s.usage.Input = max(s.usage.Input, parsed.prompt)
		s.usage.Output = max(s.usage.Output, parsed.output)
		s.usage.Seen = true
	}
	for key, value := range parsed.metadata {
		if s.metadata == nil {
			s.metadata = make(map[string]json.RawMessage)
		}
		s.metadata[key] = value
	}
	if parsed.finishReason != "" {
		s.finishReason = parsed.finishReason
	}
	if parsed.textParts == nil {
		return nil
	}
	if s.id == "" {
		generated, err := newStreamID("chatcmpl_")
		if err != nil {
			return err
		}
		s.id = generated
	}
	var text string
	for _, part := range parsed.textParts {
		text += part
	}
	return emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{"role": "assistant", "content": text}, "", nil)
}

func (s *geminiToChatStream) complete() error {
	if s.events == 0 {
		return errors.New("Gemini stream ended without events")
	}
	if s.finishReason == "" {
		return errors.New("Gemini stream ended without a finish reason")
	}
	mapped, err := mapGeminiFinishReason(s.finishReason)
	if err != nil {
		return err
	}
	if s.id == "" {
		generated, err := newStreamID("chatcmpl_")
		if err != nil {
			return err
		}
		s.id = generated
	}
	extra := map[string]any{}
	if s.usage.Seen {
		usage := map[string]any{"prompt_tokens": s.usage.Input, "completion_tokens": s.usage.Output, "total_tokens": s.usage.Input + s.usage.Output}
		if s.metadata != nil {
			if cached, ok := s.metadata["usageMetadata"]; ok {
				var fields struct {
					Cached int64 `json:"cachedContentTokenCount"`
				}
				if json.Unmarshal(cached, &fields) == nil && fields.Cached > 0 {
					usage["prompt_tokens_details"] = map[string]int64{"cached_tokens": fields.Cached}
				}
			}
		}
		extra["usage"] = usage
	}
	if len(s.metadata) > 0 {
		extra["provider_metadata"] = map[string]any{"gemini": s.metadata}
	}
	if err := emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{}, mapped, extra); err != nil {
		return err
	}
	s.usage.Complete = true
	return writeStreamDone(s.dst)
}

// Stream converts Gemini GenerateContent events into Anthropic Messages
// events with the same content restrictions as the Gemini to Chat direction.
func (messagesGemini) Stream(dst StreamWriter, src io.Reader, requestedModel string) (native.Usage, error) {
	state := &geminiToAnthropicStream{dst: dst, model: requestedModel}
	reader := newSSEReader(src)
	for {
		data, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return state.usage, err
		}
		if err := state.event(data); err != nil {
			return state.usage, err
		}
	}
	if err := state.complete(); err != nil {
		return state.usage, err
	}
	return state.usage, nil
}

type geminiToAnthropicStream struct {
	dst          StreamWriter
	model        string
	id           string
	started      bool
	finishReason string
	usage        native.Usage
	metadata     map[string]json.RawMessage
	events       int
	textOpen     bool
}

func (s *geminiToAnthropicStream) event(data []byte) error {
	parsed, err := parseGeminiStreamEvent(data)
	if err != nil {
		return err
	}
	s.events++
	if s.id == "" && parsed.responseID != "" {
		s.id = parsed.responseID
	}
	if parsed.hasUsage {
		s.usage.Input = max(s.usage.Input, parsed.prompt)
		s.usage.Output = max(s.usage.Output, parsed.output)
		s.usage.Seen = true
	}
	for key, value := range parsed.metadata {
		if s.metadata == nil {
			s.metadata = make(map[string]json.RawMessage)
		}
		s.metadata[key] = value
	}
	if parsed.finishReason != "" {
		s.finishReason = parsed.finishReason
	}
	if parsed.textParts == nil {
		return nil
	}
	if err := s.ensureStart(); err != nil {
		return err
	}
	if !s.textOpen {
		start, err := json.Marshal(map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]string{"type": "text", "text": ""},
		})
		if err != nil {
			return err
		}
		if err := writeAnthropicEvent(s.dst, "content_block_start", start); err != nil {
			return err
		}
		s.textOpen = true
	}
	for _, part := range parsed.textParts {
		delta, err := json.Marshal(map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": part},
		})
		if err != nil {
			return err
		}
		if err := writeAnthropicEvent(s.dst, "content_block_delta", delta); err != nil {
			return err
		}
	}
	return nil
}

func (s *geminiToAnthropicStream) ensureStart() error {
	if s.started {
		return nil
	}
	if s.id == "" {
		generated, err := newStreamID("msg_")
		if err != nil {
			return err
		}
		s.id = generated
	}
	start, err := json.Marshal(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": s.id, "type": "message", "role": "assistant", "model": s.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int64{"input_tokens": s.usage.Input, "output_tokens": 0},
		},
	})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "message_start", start); err != nil {
		return err
	}
	s.started = true
	return nil
}

func (s *geminiToAnthropicStream) complete() error {
	if s.events == 0 {
		return errors.New("Gemini stream ended without events")
	}
	if s.finishReason == "" {
		return errors.New("Gemini stream ended without a finish reason")
	}
	if !s.usage.Seen {
		return errors.New("Gemini stream ended without provider usage")
	}
	mapped, err := mapGeminiStopReason(s.finishReason)
	if err != nil {
		return err
	}
	if err := s.ensureStart(); err != nil {
		return err
	}
	if s.textOpen {
		stop, err := json.Marshal(map[string]any{"type": "content_block_stop", "index": 0})
		if err != nil {
			return err
		}
		if err := writeAnthropicEvent(s.dst, "content_block_stop", stop); err != nil {
			return err
		}
		s.textOpen = false
	}
	delta, err := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": mapped, "stop_sequence": nil},
		"usage": map[string]int64{"input_tokens": s.usage.Input, "output_tokens": s.usage.Output},
	})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "message_delta", delta); err != nil {
		return err
	}
	stopEvent := map[string]any{"type": "message_stop"}
	if len(s.metadata) > 0 {
		stopEvent["provider_metadata"] = map[string]any{"gemini": s.metadata}
	}
	payload, err := json.Marshal(stopEvent)
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "message_stop", payload); err != nil {
		return err
	}
	s.usage.Complete = true
	return nil
}
