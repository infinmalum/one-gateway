package bridge

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/infinmalum/one-gateway/relay/native"
)

// StreamWriter is the destination for converted event streams. Flush is called
// after every event so clients receive increments immediately.
type StreamWriter interface {
	io.Writer
	Flush()
}

// StreamConverter converts an upstream event stream into the client protocol's
// event stream. Writing the first client event commits the response, so
// converters must reject unrepresentable content as early as possible.
type StreamConverter interface {
	Converter
	Stream(dst StreamWriter, src io.Reader, requestedModel string) (native.Usage, error)
}

const maxStreamEvent = 2 << 20

type sseReader struct {
	scanner *bufio.Scanner
	pending []byte
}

func newSSEReader(src io.Reader) *sseReader {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStreamEvent)
	return &sseReader{scanner: scanner}
}

// next returns the concatenated data lines of the next SSE event. Lines other
// than data are ignored because every supported protocol carries its event
// type inside the data JSON. A stream that ends with an undelivered event
// still returns it.
func (r *sseReader) next() ([]byte, error) {
	for r.scanner.Scan() {
		line := bytes.TrimSuffix(r.scanner.Bytes(), []byte{'\r'})
		if len(line) == 0 {
			if len(r.pending) == 0 {
				continue
			}
			data := r.pending
			r.pending = nil
			return data, nil
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		value := bytes.TrimPrefix(line, []byte("data:"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if len(r.pending)+len(value)+1 > maxStreamEvent {
			return nil, errors.New("stream event exceeds the conversion size limit")
		}
		if len(r.pending) > 0 {
			r.pending = append(r.pending, '\n')
		}
		r.pending = append(r.pending, value...)
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	if len(r.pending) > 0 {
		data := r.pending
		r.pending = nil
		return data, nil
	}
	return nil, io.EOF
}

func writeStreamEvent(dst StreamWriter, payload []byte) error {
	buffer := make([]byte, 0, len(payload)+8)
	buffer = append(buffer, "data: "...)
	buffer = append(buffer, payload...)
	buffer = append(buffer, '\n', '\n')
	if _, err := dst.Write(buffer); err != nil {
		return err
	}
	dst.Flush()
	return nil
}

func writeStreamDone(dst StreamWriter) error {
	if _, err := dst.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	dst.Flush()
	return nil
}

func writeAnthropicEvent(dst StreamWriter, name string, payload []byte) error {
	buffer := make([]byte, 0, len(name)+len(payload)+16)
	buffer = append(buffer, "event: "...)
	buffer = append(buffer, name...)
	buffer = append(buffer, '\n')
	buffer = append(buffer, "data: "...)
	buffer = append(buffer, payload...)
	buffer = append(buffer, '\n', '\n')
	if _, err := dst.Write(buffer); err != nil {
		return err
	}
	dst.Flush()
	return nil
}

func writeStreamError(dst StreamWriter, message string) error {
	payload, err := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": "gateway_stream_error", "code": "conversion_failed"}})
	if err != nil {
		return err
	}
	return writeStreamEvent(dst, payload)
}

func emitChatChunk(dst StreamWriter, id string, created int64, model string, delta map[string]any, finish string, extra map[string]any) error {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	envelope := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{choice},
	}
	for key, value := range extra {
		envelope[key] = value
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return writeStreamEvent(dst, payload)
}

func mapAnthropicStopReason(reason string) (string, error) {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop", nil
	case "max_tokens":
		return "length", nil
	case "tool_use":
		return "tool_calls", nil
	default:
		return "", fmt.Errorf("Anthropic stop reason %q cannot be converted", reason)
	}
}

func mapChatFinishReason(reason string) (string, error) {
	switch reason {
	case "stop":
		return "end_turn", nil
	case "length":
		return "max_tokens", nil
	case "tool_calls":
		return "tool_use", nil
	default:
		return "", fmt.Errorf("Chat finish_reason %q cannot be converted", reason)
	}
}

func mapGeminiFinishReason(reason string, toolCalls bool) (string, error) {
	switch reason {
	case "STOP":
		if toolCalls {
			return "tool_calls", nil
		}
		return "stop", nil
	case "MAX_TOKENS":
		return "length", nil
	default:
		return "", fmt.Errorf("Gemini finish reason %q cannot be converted", reason)
	}
}

func mapGeminiStopReason(reason string, toolCalls bool) (string, error) {
	switch reason {
	case "STOP":
		if toolCalls {
			return "tool_use", nil
		}
		return "end_turn", nil
	case "MAX_TOKENS":
		return "max_tokens", nil
	default:
		return "", fmt.Errorf("Gemini finish reason %q cannot be converted", reason)
	}
}

func newStreamID(prefix string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("failed to create stream response ID")
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

// rawObject returns the top-level keys of a JSON object. Keys outside the
// permitted list are collected into extra so provider envelope extensions are
// never dropped from converted streams.
func rawObject(data []byte, permitted ...string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(data, &value) != nil || value == nil {
		return nil, nil, errors.New("expected a JSON object")
	}
	allowed := make(map[string]bool, len(permitted))
	for _, key := range permitted {
		allowed[key] = true
	}
	var extra map[string]json.RawMessage
	for key, raw := range value {
		if !allowed[key] {
			if extra == nil {
				extra = make(map[string]json.RawMessage)
			}
			extra[key] = raw
		}
	}
	return value, extra, nil
}

func streamTimestamp() int64 {
	return time.Now().Unix()
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func nonNull(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "null"
}

// decodeStreamNumber reads a non-negative integer field that may be absent.
func decodeStreamNumber(raw json.RawMessage) (int64, bool) {
	if !nonNull(raw) {
		return 0, true
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func parseIntField(raw json.RawMessage, name string) (int, error) {
	if !nonNull(raw) {
		return 0, errors.New(name + " is required")
	}
	value, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}
