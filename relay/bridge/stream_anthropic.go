package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/infinmalum/one-gateway/relay/native"
)

// Stream converts Anthropic Messages events into Chat Completions chunks.
// Thinking, citations, and server tools cannot be represented and fail the
// stream instead of being dropped.
func (chatAnthropic) Stream(dst StreamWriter, src io.Reader, requestedModel string) (native.Usage, error) {
	state := &anthropicToChatStream{dst: dst, model: requestedModel, created: streamTimestamp(), blockTools: map[int]int{}}
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
	if !state.usage.Complete {
		return state.usage, errors.New("Anthropic stream ended before message_stop")
	}
	return state.usage, writeStreamDone(dst)
}

type anthropicToChatStream struct {
	dst        StreamWriter
	model      string
	created    int64
	id         string
	started    bool
	stopSeen   bool
	toolCount  int
	blockTools map[int]int
	usage      native.Usage
}

func (s *anthropicToChatStream) event(data []byte) error {
	if s.usage.Complete {
		return errors.New("Anthropic stream sent an event after message_stop")
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &header) != nil || header.Type == "" {
		return errors.New("invalid Anthropic event")
	}
	switch header.Type {
	case "message_start":
		return s.messageStart(data)
	case "content_block_start":
		return s.blockStart(data)
	case "content_block_delta":
		return s.blockDelta(data)
	case "content_block_stop":
		if _, extra, err := rawObject(data, "type", "index"); err != nil {
			return err
		} else if len(extra) > 0 {
			return errors.New("Anthropic content_block_stop carries fields Chat cannot represent")
		}
		return nil
	case "message_delta":
		return s.messageDelta(data)
	case "message_stop":
		if _, extra, err := rawObject(data, "type"); err != nil {
			return err
		} else if len(extra) > 0 {
			return errors.New("Anthropic message_stop carries fields Chat cannot represent")
		}
		if !s.started || !s.stopSeen {
			return errors.New("Anthropic message_stop arrived before a finish reason")
		}
		s.usage.Complete = true
		return nil
	case "ping":
		return nil
	case "error":
		fields, extra, err := rawObject(data, "type", "error")
		if err != nil {
			return fmt.Errorf("invalid Anthropic error event: %w", err)
		}
		if len(extra) > 0 {
			return errors.New("Anthropic error event carries fields Chat cannot represent")
		}
		failure, _, err := rawObject(fields["error"], "type", "message")
		if err != nil {
			return fmt.Errorf("invalid Anthropic error event: %w", err)
		}
		if message := rawString(failure["message"]); message != "" {
			return errors.New(message)
		}
		return errors.New("upstream Anthropic stream failed")
	default:
		return fmt.Errorf("Anthropic event %q cannot be converted", header.Type)
	}
}

func (s *anthropicToChatStream) messageStart(data []byte) error {
	if s.started {
		return errors.New("Anthropic stream sent message_start twice")
	}
	fields, extra, err := rawObject(data, "type", "message")
	if err != nil {
		return fmt.Errorf("invalid Anthropic message_start: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic message_start field %q cannot be converted", firstKey(extra))
	}
	message, extra, err := rawObject(fields["message"], "id", "type", "role", "model", "content", "stop_reason", "stop_sequence", "usage")
	if err != nil {
		return fmt.Errorf("invalid Anthropic message_start: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic message_start field %q cannot be converted", firstKey(extra))
	}
	s.id = rawString(message["id"])
	if s.id == "" {
		return errors.New("Anthropic message_start requires an id")
	}
	if role := rawString(message["role"]); role != "" && role != "assistant" {
		return errors.New("Anthropic response role must be assistant")
	}
	if nonNull(message["stop_reason"]) || nonNull(message["stop_sequence"]) {
		return errors.New("Anthropic message_start cannot contain a stop reason")
	}
	if nonNull(message["content"]) {
		var initialBlocks []json.RawMessage
		if json.Unmarshal(message["content"], &initialBlocks) != nil || len(initialBlocks) != 0 {
			return errors.New("Anthropic message_start content must be empty")
		}
	}
	if len(message["usage"]) != 0 {
		if err := s.observeInputUsage(message["usage"]); err != nil {
			return err
		}
	}
	s.started = true
	return emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{"role": "assistant", "content": ""}, "", nil)
}

func (s *anthropicToChatStream) observeInputUsage(raw json.RawMessage) error {
	usage, extra, err := rawObject(raw, "input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")
	if err != nil {
		return fmt.Errorf("invalid Anthropic usage: %w", err)
	}
	if len(extra) > 0 {
		return errors.New("Anthropic usage carries fields Chat cannot represent")
	}
	input, ok := decodeStreamNumber(usage["input_tokens"])
	if !ok || !nonNull(usage["input_tokens"]) {
		return errors.New("Anthropic usage requires input_tokens")
	}
	cacheCreation, ok := decodeStreamNumber(usage["cache_creation_input_tokens"])
	if !ok {
		return errors.New("invalid Anthropic usage")
	}
	cacheRead, ok := decodeStreamNumber(usage["cache_read_input_tokens"])
	if !ok {
		return errors.New("invalid Anthropic usage")
	}
	s.usage.Input = max(s.usage.Input, input+cacheCreation+cacheRead)
	s.usage.Seen = true
	return nil
}

func (s *anthropicToChatStream) blockStart(data []byte) error {
	if !s.started {
		return errors.New("Anthropic stream sent content before message_start")
	}
	fields, extra, err := rawObject(data, "type", "index", "content_block")
	if err != nil {
		return fmt.Errorf("invalid Anthropic content_block_start: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic content_block_start field %q cannot be converted", firstKey(extra))
	}
	index, err := parseIntField(fields["index"], "block index")
	if err != nil {
		return err
	}
	block, extra, err := rawObject(fields["content_block"], "type", "text", "id", "name", "input")
	if err != nil {
		return fmt.Errorf("invalid Anthropic content block: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic content block field %q cannot be converted", firstKey(extra))
	}
	switch rawString(block["type"]) {
	case "text":
		if len(block) != 2 {
			return errors.New("invalid Anthropic text block")
		}
		var initial string
		if json.Unmarshal(block["text"], &initial) != nil {
			return errors.New("Anthropic text block requires text")
		}
		if initial != "" {
			return emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{"content": initial}, "", nil)
		}
		return nil
	case "tool_use":
		if len(block) != 4 {
			return errors.New("invalid Anthropic tool_use block")
		}
		id, name := rawString(block["id"]), rawString(block["name"])
		if id == "" || name == "" {
			return errors.New("Anthropic tool_use requires an id and name")
		}
		toolIndex := s.toolCount
		s.toolCount++
		s.blockTools[index] = toolIndex
		delta := map[string]any{"tool_calls": []any{map[string]any{
			"index": toolIndex, "id": id, "type": "function",
			"function": map[string]string{"name": name, "arguments": ""},
		}}}
		return emitChatChunk(s.dst, s.id, s.created, s.model, delta, "", nil)
	default:
		return fmt.Errorf("Anthropic content block %q cannot be converted", rawString(block["type"]))
	}
}

func (s *anthropicToChatStream) blockDelta(data []byte) error {
	if !s.started {
		return errors.New("Anthropic stream sent content before message_start")
	}
	fields, extra, err := rawObject(data, "type", "index", "delta")
	if err != nil {
		return fmt.Errorf("invalid Anthropic content_block_delta: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic content_block_delta field %q cannot be converted", firstKey(extra))
	}
	index, err := parseIntField(fields["index"], "block index")
	if err != nil {
		return err
	}
	delta, extra, err := rawObject(fields["delta"], "type", "text", "partial_json")
	if err != nil {
		return fmt.Errorf("invalid Anthropic content delta: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic delta field %q cannot be converted", firstKey(extra))
	}
	switch rawString(delta["type"]) {
	case "text_delta":
		if len(delta) != 2 {
			return errors.New("invalid Anthropic text delta")
		}
		return emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{"content": rawString(delta["text"])}, "", nil)
	case "input_json_delta":
		if len(delta) != 2 {
			return errors.New("invalid Anthropic tool delta")
		}
		toolIndex, ok := s.blockTools[index]
		if !ok {
			return errors.New("Anthropic tool delta arrived before its tool_use block")
		}
		toolDelta := map[string]any{"tool_calls": []any{map[string]any{
			"index":    toolIndex,
			"function": map[string]string{"arguments": rawString(delta["partial_json"])},
		}}}
		return emitChatChunk(s.dst, s.id, s.created, s.model, toolDelta, "", nil)
	default:
		return fmt.Errorf("Anthropic delta %q cannot be converted", rawString(delta["type"]))
	}
}

func (s *anthropicToChatStream) messageDelta(data []byte) error {
	if !s.started {
		return errors.New("Anthropic stream sent message_delta before message_start")
	}
	fields, extra, err := rawObject(data, "type", "delta", "usage")
	if err != nil {
		return fmt.Errorf("invalid Anthropic message_delta: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic message_delta field %q cannot be converted", firstKey(extra))
	}
	delta, extra, err := rawObject(fields["delta"], "type", "stop_reason", "stop_sequence")
	if err != nil {
		return fmt.Errorf("invalid Anthropic message delta: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Anthropic message delta field %q cannot be converted", firstKey(extra))
	}
	if nonNull(delta["stop_sequence"]) {
		return errors.New("Anthropic stop_sequence cannot be converted")
	}
	if len(fields["usage"]) != 0 {
		if err := s.observeOutputUsage(fields["usage"]); err != nil {
			return err
		}
	}
	finish := ""
	if reason := rawString(delta["stop_reason"]); reason != "" {
		mapped, err := mapAnthropicStopReason(reason)
		if err != nil {
			return err
		}
		finish = mapped
		s.stopSeen = true
	}
	return emitChatChunk(s.dst, s.id, s.created, s.model, map[string]any{}, finish, nil)
}

func (s *anthropicToChatStream) observeOutputUsage(raw json.RawMessage) error {
	usage, extra, err := rawObject(raw, "input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")
	if err != nil {
		return fmt.Errorf("invalid Anthropic usage: %w", err)
	}
	if len(extra) > 0 {
		return errors.New("Anthropic usage carries fields Chat cannot represent")
	}
	if nonNull(usage["input_tokens"]) {
		input, ok := decodeStreamNumber(usage["input_tokens"])
		if !ok {
			return errors.New("invalid Anthropic usage")
		}
		cacheCreation, ok := decodeStreamNumber(usage["cache_creation_input_tokens"])
		if !ok {
			return errors.New("invalid Anthropic usage")
		}
		cacheRead, ok := decodeStreamNumber(usage["cache_read_input_tokens"])
		if !ok {
			return errors.New("invalid Anthropic usage")
		}
		s.usage.Input = max(s.usage.Input, input+cacheCreation+cacheRead)
		s.usage.Seen = true
	}
	if nonNull(usage["output_tokens"]) {
		output, ok := decodeStreamNumber(usage["output_tokens"])
		if !ok {
			return errors.New("invalid Anthropic usage")
		}
		s.usage.Output = max(s.usage.Output, output)
		s.usage.Seen = true
	}
	return nil
}

// Stream converts Chat Completions chunks into Anthropic Messages events. The
// terminal events are written only after the upstream [DONE] marker so an
// interrupted upstream stream never looks complete to the client.
func (anthropicChat) Stream(dst StreamWriter, src io.Reader, requestedModel string) (native.Usage, error) {
	state := &chatToAnthropicStream{dst: dst, model: requestedModel, toolBlocks: map[int]int{}}
	reader := newSSEReader(src)
	for {
		data, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return state.usage, err
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			if err := state.finish(); err != nil {
				return state.usage, err
			}
			return state.usage, nil
		}
		if err := state.chunk(data); err != nil {
			return state.usage, err
		}
	}
	if state.finished {
		return state.usage, nil
	}
	return state.usage, errors.New("Chat stream ended before [DONE]")
}

type chatToAnthropicStream struct {
	dst        StreamWriter
	model      string
	id         string
	started    bool
	textOpen   bool
	textBlock  int
	toolBlocks map[int]int
	toolOrder  []int
	nextBlock  int
	stopReason string
	usage      native.Usage
	inputSeen  bool
	outputSeen bool
	metadata   map[string]json.RawMessage
	finished   bool
}

func (s *chatToAnthropicStream) chunk(data []byte) error {
	fields, extra, err := rawObject(data, "id", "object", "created", "model", "choices", "usage")
	if err != nil {
		return fmt.Errorf("invalid Chat chunk: %w", err)
	}
	for key, value := range extra {
		if s.metadata == nil {
			s.metadata = make(map[string]json.RawMessage)
		}
		s.metadata[key] = value
	}
	if id := rawString(fields["id"]); id != "" {
		s.id = id
	}
	var choices []json.RawMessage
	if json.Unmarshal(fields["choices"], &choices) != nil {
		return errors.New("Chat chunk choices must be an array")
	}
	if len(choices) > 1 {
		return errors.New("Chat chunk must contain one choice")
	}
	if nonNull(fields["usage"]) {
		if err := s.observeUsage(fields["usage"]); err != nil {
			return err
		}
	}
	if len(choices) == 0 {
		return nil
	}
	choice, choiceExtra, err := rawObject(choices[0], "index", "delta", "finish_reason", "logprobs")
	if err != nil {
		return fmt.Errorf("invalid Chat choice: %w", err)
	}
	if len(choiceExtra) > 0 {
		return fmt.Errorf("Chat choice field %q cannot be converted", firstKey(choiceExtra))
	}
	if index, err := parseIntField(choice["index"], "choice index"); err != nil || index != 0 {
		return errors.New("Chat response choice index must be zero")
	}
	if nonNull(choice["logprobs"]) {
		return errors.New("Chat logprobs cannot be converted")
	}
	if finish := rawString(choice["finish_reason"]); finish != "" {
		mapped, err := mapChatFinishReason(finish)
		if err != nil {
			return err
		}
		s.stopReason = mapped
	}
	if !nonNull(choice["delta"]) {
		return nil
	}
	return s.delta(choice["delta"])
}

func (s *chatToAnthropicStream) observeUsage(raw json.RawMessage) error {
	usage, extra, err := rawObject(raw, "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details")
	if err != nil {
		return fmt.Errorf("invalid Chat usage: %w", err)
	}
	if len(extra) > 0 {
		return errors.New("Chat usage carries fields Messages cannot represent")
	}
	if nonNull(usage["prompt_tokens"]) {
		prompt, ok := decodeStreamNumber(usage["prompt_tokens"])
		if !ok {
			return errors.New("invalid Chat usage")
		}
		s.usage.Input = max(s.usage.Input, prompt)
		s.usage.Seen = true
		s.inputSeen = true
	}
	if nonNull(usage["completion_tokens"]) {
		completion, ok := decodeStreamNumber(usage["completion_tokens"])
		if !ok {
			return errors.New("invalid Chat usage")
		}
		s.usage.Output = max(s.usage.Output, completion)
		s.usage.Seen = true
		s.outputSeen = true
	}
	return nil
}

func (s *chatToAnthropicStream) delta(raw json.RawMessage) error {
	delta, extra, err := rawObject(raw, "role", "content", "tool_calls", "refusal")
	if err != nil {
		return fmt.Errorf("invalid Chat delta: %w", err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("Chat delta field %q cannot be converted", firstKey(extra))
	}
	if role := rawString(delta["role"]); role != "" && role != "assistant" {
		return errors.New("Chat response role must be assistant")
	}
	if nonNull(delta["refusal"]) {
		return errors.New("Chat refusal cannot be converted")
	}
	if nonNull(delta["content"]) {
		var text string
		if json.Unmarshal(delta["content"], &text) != nil {
			return errors.New("Chat content must be text")
		}
		if text != "" {
			if err := s.openText(); err != nil {
				return err
			}
			if err := s.emitTextDelta(text); err != nil {
				return err
			}
		}
	}
	if nonNull(delta["tool_calls"]) {
		var calls []json.RawMessage
		if json.Unmarshal(delta["tool_calls"], &calls) != nil {
			return errors.New("Chat tool_calls must be an array")
		}
		for _, callRaw := range calls {
			if err := s.toolCall(callRaw); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *chatToAnthropicStream) toolCall(callRaw json.RawMessage) error {
	call, extra, err := rawObject(callRaw, "index", "id", "type", "function")
	if err != nil {
		return fmt.Errorf("invalid Chat tool call: %w", err)
	}
	if len(extra) > 0 {
		return errors.New("Chat tool call carries fields Messages cannot represent")
	}
	index, err := parseIntField(call["index"], "tool call index")
	if err != nil {
		return err
	}
	blockIndex, seen := s.toolBlocks[index]
	function, _, err := rawObject(call["function"], "name", "arguments")
	if err != nil {
		return fmt.Errorf("invalid Chat tool call: %w", err)
	}
	if !seen {
		id, kind, name := rawString(call["id"]), rawString(call["type"]), rawString(function["name"])
		if id == "" || kind != "function" || name == "" {
			return errors.New("only named function tool calls can be converted")
		}
		if err := s.ensureStart(); err != nil {
			return err
		}
		blockIndex = s.nextBlock
		s.nextBlock++
		s.toolBlocks[index] = blockIndex
		s.toolOrder = append(s.toolOrder, blockIndex)
		if s.textOpen {
			if err := s.closeBlock(s.textBlock); err != nil {
				return err
			}
			s.textOpen = false
		}
		start, err := json.Marshal(map[string]any{
			"type": "content_block_start", "index": blockIndex,
			"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
		})
		if err != nil {
			return err
		}
		if err := writeAnthropicEvent(s.dst, "content_block_start", start); err != nil {
			return err
		}
	}
	if arguments := rawString(function["arguments"]); arguments != "" {
		if err := s.emitToolDelta(blockIndex, arguments); err != nil {
			return err
		}
	}
	return nil
}

func (s *chatToAnthropicStream) openText() error {
	if s.started && s.textOpen {
		return nil
	}
	if err := s.ensureStart(); err != nil {
		return err
	}
	s.textBlock = s.nextBlock
	s.nextBlock++
	start, err := json.Marshal(map[string]any{
		"type": "content_block_start", "index": s.textBlock,
		"content_block": map[string]string{"type": "text", "text": ""},
	})
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "content_block_start", start); err != nil {
		return err
	}
	s.textOpen = true
	return nil
}

func (s *chatToAnthropicStream) closeBlock(index int) error {
	stop, err := json.Marshal(map[string]any{"type": "content_block_stop", "index": index})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_stop", stop)
}

func (s *chatToAnthropicStream) emitTextDelta(text string) error {
	if err := s.ensureStart(); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": s.textBlock,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_delta", payload)
}

func (s *chatToAnthropicStream) emitToolDelta(blockIndex int, arguments string) error {
	payload, err := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": blockIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": arguments},
	})
	if err != nil {
		return err
	}
	return writeAnthropicEvent(s.dst, "content_block_delta", payload)
}

func (s *chatToAnthropicStream) ensureStart() error {
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

func (s *chatToAnthropicStream) finish() error {
	if s.finished {
		return nil
	}
	if !s.started {
		return errors.New("Chat stream ended without content")
	}
	if s.stopReason == "" {
		return errors.New("Chat stream ended without a finish reason")
	}
	if !s.inputSeen || !s.outputSeen {
		return errors.New("Chat stream ended without complete provider usage")
	}
	if s.textOpen {
		if err := s.closeBlock(s.textBlock); err != nil {
			return err
		}
		s.textOpen = false
	}
	for _, blockIndex := range s.toolOrder {
		if err := s.closeBlock(blockIndex); err != nil {
			return err
		}
	}
	delta, err := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil},
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
		stopEvent["provider_metadata"] = map[string]any{"openai": s.metadata}
	}
	payload, err := json.Marshal(stopEvent)
	if err != nil {
		return err
	}
	if err := writeAnthropicEvent(s.dst, "message_stop", payload); err != nil {
		return err
	}
	s.finished = true
	s.usage.Complete = true
	return nil
}

func firstKey(values map[string]json.RawMessage) string {
	for key := range values {
		return key
	}
	return ""
}
