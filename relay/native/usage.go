package native

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// Usage contains the provider-reported token counts. Seen is false when no
// usage report was received, so callers can choose a conservative fallback.
type Usage struct {
	Input  int64
	Output int64
	Seen   bool
	// Complete marks a protocol-level end event where one exists.
	Complete bool
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type geminiUsage struct {
	PromptTokenCount     int64 `json:"promptTokenCount"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	TotalTokenCount      int64 `json:"totalTokenCount"`
}

type openAIUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func (u *Usage) observe(protocol Protocol, data []byte) {
	switch protocol {
	case Anthropic:
		var result struct {
			Type    string          `json:"type"`
			Usage   *anthropicUsage `json:"usage"`
			Message *struct {
				Usage *anthropicUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(data, &result) != nil {
			return
		}
		if result.Type == "message_stop" {
			u.Complete = true
		}
		usage := result.Usage
		if result.Message != nil && result.Message.Usage != nil {
			usage = result.Message.Usage
		}
		if usage == nil {
			return
		}
		input := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		u.Input = max(u.Input, input)
		u.Output = max(u.Output, usage.OutputTokens)
		u.Seen = true
	case Gemini:
		var result struct {
			Usage *geminiUsage `json:"usageMetadata"`
		}
		if json.Unmarshal(data, &result) != nil || result.Usage == nil {
			return
		}
		u.Input = max(u.Input, result.Usage.PromptTokenCount)
		output := result.Usage.CandidatesTokenCount + result.Usage.ThoughtsTokenCount
		if result.Usage.TotalTokenCount > result.Usage.PromptTokenCount {
			output = max(output, result.Usage.TotalTokenCount-result.Usage.PromptTokenCount)
		}
		u.Output = max(u.Output, output)
		u.Seen = true
	case OpenAIResponses:
		var result struct {
			Type     string       `json:"type"`
			Usage    *openAIUsage `json:"usage"`
			Response *struct {
				Usage *openAIUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &result) != nil {
			return
		}
		switch result.Type {
		case "response.completed", "response.failed", "response.incomplete":
			u.Complete = true
		}
		usage := result.Usage
		if result.Response != nil && result.Response.Usage != nil {
			usage = result.Response.Usage
		}
		if usage != nil {
			u.Input = max(u.Input, usage.InputTokens)
			u.Output = max(u.Output, usage.OutputTokens)
			u.Seen = true
		}
	case OpenAIChat, OpenAICompletions:
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			u.Complete = true
			return
		}
		var result struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &result) == nil && result.Usage != nil {
			u.Input = max(u.Input, result.Usage.PromptTokens)
			u.Output = max(u.Output, result.Usage.CompletionTokens)
			u.Seen = true
		}
	case OpenAIEmbeddings:
		var result struct {
			Usage *struct {
				PromptTokens int64 `json:"prompt_tokens"`
				TotalTokens  int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &result) == nil && result.Usage != nil {
			u.Input = max(result.Usage.PromptTokens, result.Usage.TotalTokens)
			u.Seen = true
		}
	}
}

// CopyResponse forwards provider bytes unchanged while observing usage. It
// flushes SSE chunks as they arrive and never reconstructs provider events.
func CopyResponse(dst http.ResponseWriter, src io.Reader, protocol Protocol, stream bool) (Usage, error) {
	var usage Usage
	if !stream {
		return copyJSONResponse(dst, src, protocol)
	}
	observer := &sseObserver{protocol: protocol, usage: &usage}
	_, err := io.Copy(&observingWriter{dst: dst, observer: observer}, src)
	if protocol == Gemini && err == nil {
		usage.Complete = true
	}
	return usage, err
}

type errorRecordingWriter struct {
	dst http.ResponseWriter
	err error
}

type errorRecordingReader struct {
	src io.Reader
	err error
}

func (r *errorRecordingReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func (w *errorRecordingWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.dst.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

// copyJSONResponse decodes only top-level usage while forwarding the entire
// response. Large embedding arrays therefore do not need to be buffered.
func copyJSONResponse(dst http.ResponseWriter, src io.Reader, protocol Protocol) (Usage, error) {
	var usage Usage
	writer := &errorRecordingWriter{dst: dst}
	reader := &errorRecordingReader{src: src}
	tee := io.TeeReader(reader, writer)
	decoder := json.NewDecoder(tee)
	if token, err := decoder.Token(); err == nil && token == json.Delim('{') {
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				break
			}
			name, ok := key.(string)
			if !ok {
				break
			}
			if name == "usage" || name == "usageMetadata" {
				var raw json.RawMessage
				if decoder.Decode(&raw) != nil {
					break
				}
				wrapped := append(append([]byte(`{"`+name+`":`), raw...), '}')
				usage.observe(protocol, wrapped)
			} else if skipJSONValue(decoder) != nil {
				break
			}
		}
	}
	_, err := io.Copy(io.Discard, tee)
	if writer.err != nil {
		err = writer.err
	} else if reader.err != nil {
		err = reader.err
	}
	usage.Complete = err == nil
	return usage, err
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		if nested, ok := token.(json.Delim); ok {
			switch nested {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

type observingWriter struct {
	dst      http.ResponseWriter
	observer *sseObserver
}

func (w *observingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.observer.write(p[:n])
	if flusher, ok := w.dst.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

type sseObserver struct {
	protocol   Protocol
	usage      *Usage
	line       []byte
	data       []byte
	discarding bool
}

const maxObservedEvent = 2 << 20

func (o *sseObserver) write(p []byte) {
	for len(p) > 0 {
		index := bytes.IndexByte(p, '\n')
		if index < 0 {
			o.appendLine(p)
			return
		}
		o.appendLine(p[:index])
		if !o.discarding {
			o.completeLine()
		} else {
			o.line = nil
			o.data = nil
			o.discarding = false
		}
		p = p[index+1:]
	}
}

func (o *sseObserver) appendLine(p []byte) {
	if o.discarding {
		return
	}
	if len(o.line)+len(p) > maxObservedEvent {
		o.discarding = true
		o.line = nil
		o.data = nil
		return
	}
	o.line = append(o.line, p...)
}

func (o *sseObserver) completeLine() {
	line := bytes.TrimSuffix(o.line, []byte{'\r'})
	if len(line) == 0 {
		if len(o.data) > 0 {
			o.usage.observe(o.protocol, o.data)
		}
		o.data = nil
	} else if bytes.HasPrefix(line, []byte("data:")) {
		value := bytes.TrimPrefix(line, []byte("data:"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if len(o.data)+len(value)+1 <= maxObservedEvent {
			if len(o.data) > 0 {
				o.data = append(o.data, '\n')
			}
			o.data = append(o.data, value...)
		} else {
			o.data = nil
		}
	}
	o.line = nil
}
