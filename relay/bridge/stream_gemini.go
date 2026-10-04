package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/infinmalum/one-gateway/relay/native"
)

// geminiStreamEvent holds the converted view of one streaming
// GenerateContentResponse. Unrepresentable parts and unknown candidate fields
// are rejected by the parser so neither stream direction can drop them.
type geminiStreamEvent struct {
	parts        []geminiPart
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
		if _, err := mapGeminiFinishReason(reason, false); err != nil {
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
		part, err := parseGeminiResponsePart(rawPart)
		if err != nil {
			return nil, err
		}
		event.parts = append(event.parts, part)
	}
	return event, nil
}

// Stream converts Gemini GenerateContent events into Chat Completions chunks.
// Function calls become tool call deltas; thought parts have no Chat
// representation and fail the stream. The stream ends when the upstream
// connection closes; Gemini has no terminal event, so a missing finish reason
// fails the conversion.
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
	toolCount    int
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
	var text string
	for _, part := range parsed.parts {
		switch part.kind {
		case geminiPartText:
			text += part.text
		case geminiPartFunctionCall:
			if err := s.emitText(text); err != nil {
				return err
			}
			text = ""
			if err := s.emitToolCall(part); err != nil {
				return err
			}
		default:
			return errors.New("Gemini thought parts cannot be represented in Chat")
		}
	}
	return s.emitText(text)
}

func (s *geminiToChatStream) emitText(text string) error {
	if text == "" {
		return nil
	}
	if s.id == "" {
		generated, err := newStreamID("chatcmpl_")
		if err != nil {
			return err
		}
		s.id = generated
	}
	return emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{"role": "assistant", "content": text}, "", nil)
}

func (s *geminiToChatStream) emitToolCall(part geminiPart) error {
	if s.id == "" {
		generated, err := newStreamID("chatcmpl_")
		if err != nil {
			return err
		}
		s.id = generated
	}
	callID := part.callID
	if callID == "" {
		generated, err := newStreamID("call_")
		if err != nil {
			return err
		}
		callID = generated
	}
	toolIndex := s.toolCount
	s.toolCount++
	delta := map[string]any{"tool_calls": []any{map[string]any{
		"index": toolIndex, "id": callID, "type": "function",
		"function": map[string]string{"name": part.name, "arguments": string(part.args)},
	}}}
	return emitChatChunk(s.dst, s.id, s.created, s.model, delta, "", nil)
}

func (s *geminiToChatStream) complete() error {
	if s.events == 0 {
		return errors.New("Gemini stream ended without events")
	}
	if s.finishReason == "" {
		return errors.New("Gemini stream ended without a finish reason")
	}
	mapped, err := mapGeminiFinishReason(s.finishReason, s.toolCount > 0)
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
// events. Function calls become tool_use blocks and thought parts become
// thinking deltas carrying the thought signature.
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
	nextBlock    int
	textOpen     bool
	thinkingOpen bool
	toolCalls    int
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
	for _, part := range parsed.parts {
		switch part.kind {
		case geminiPartText:
			if err := s.closeThinking(); err != nil {
				return err
			}
			if err := s.openText(); err != nil {
				return err
			}
			if err := s.emitTextDelta(part.text); err != nil {
				return err
			}
		case geminiPartThought:
			if s.textOpen {
				if err := s.closeBlock(); err != nil {
					return err
				}
				s.textOpen = false
			}
			if !s.thinkingOpen {
				if err := s.openThinking(); err != nil {
					return err
				}
			}
			if part.text != "" {
				if err := s.emitThinkingDelta(part.text); err != nil {
					return err
				}
			}
			if part.signature != "" {
				if err := s.emitSignatureDelta(part.signature); err != nil {
					return err
				}
				s.thinkingOpen = false
			}
		case geminiPartSignature:
			if !s.thinkingOpen {
				return errors.New("Gemini thought signature has no open thinking block")
			}
			if err := s.emitSignatureDelta(part.signature); err != nil {
				return err
			}
			s.thinkingOpen = false
		case geminiPartFunctionCall:
			if s.textOpen {
				if err := s.closeBlock(); err != nil {
					return err
				}
				s.textOpen = false
			}
			if err := s.closeThinking(); err != nil {
				return err
			}
			if err := s.emitToolUse(part); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *geminiToAnthropicStream) openText() error {
	if s.textOpen {
		return nil
	}
	if err := s.ensureStart(); err != nil {
		return err
	}
	start, err := json.Marshal(map[string]any{
		"type": "content_block_start", "index": s.nextBlock,
		"content_block": map[string]string{"type": "text", "text": ""},
	})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "content_block_start", start); err != nil {
		return err
	}
	s.textOpen = true
	s.nextBlock++
	return nil
}

func (s *geminiToAnthropicStream) openThinking() error {
	if s.thinkingOpen {
		return nil
	}
	if err := s.ensureStart(); err != nil {
		return err
	}
	start, err := json.Marshal(map[string]any{
		"type": "content_block_start", "index": s.nextBlock,
		"content_block": map[string]string{"type": "thinking", "thinking": ""},
	})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "content_block_start", start); err != nil {
		return err
	}
	s.thinkingOpen = true
	s.nextBlock++
	return nil
}

func (s *geminiToAnthropicStream) closeThinking() error {
	if !s.thinkingOpen {
		return nil
	}
	if err := s.closeBlock(); err != nil {
		return err
	}
	s.thinkingOpen = false
	return nil
}

func (s *geminiToAnthropicStream) closeBlock() error {
	stop, err := json.Marshal(map[string]any{"type": "content_block_stop", "index": s.nextBlock - 1})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_stop", stop)
}

func (s *geminiToAnthropicStream) emitTextDelta(text string) error {
	payload, err := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": s.nextBlock - 1,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_delta", payload)
}

func (s *geminiToAnthropicStream) emitThinkingDelta(text string) error {
	payload, err := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": s.nextBlock - 1,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_delta", payload)
}

func (s *geminiToAnthropicStream) emitSignatureDelta(signature string) error {
	payload, err := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": s.nextBlock - 1,
		"delta": map[string]any{"type": "signature_delta", "signature": signature},
	})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_delta", payload)
}

func (s *geminiToAnthropicStream) emitToolUse(part geminiPart) error {
	if err := s.ensureStart(); err != nil {
		return err
	}
	blockID := part.callID
	if blockID == "" {
		generated, err := newStreamID("toolu_")
		if err != nil {
			return err
		}
		blockID = generated
	}
	index := s.nextBlock
	s.nextBlock++
	start, err := json.Marshal(map[string]any{
		"type": "content_block_start", "index": index,
		"content_block": map[string]any{"type": "tool_use", "id": blockID, "name": part.name, "input": map[string]any{}},
	})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "content_block_start", start); err != nil {
		return err
	}
	arguments, err := json.Marshal(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(part.args)}})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "content_block_delta", arguments); err != nil {
		return err
	}
	stop, err := json.Marshal(map[string]any{"type": "content_block_stop", "index": index})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "content_block_stop", stop); err != nil {
		return err
	}
	s.toolCalls++
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
	mapped, err := mapGeminiStopReason(s.finishReason, s.toolCalls > 0)
	if err != nil {
		return err
	}
	if err := s.ensureStart(); err != nil {
		return err
	}
	if err := s.closeThinking(); err != nil {
		return err
	}
	if s.textOpen {
		if err := s.closeBlock(); err != nil {
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
