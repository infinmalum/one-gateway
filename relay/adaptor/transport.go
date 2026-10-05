package adaptor

import (
	"encoding/json"
	"io"
	"net/http"
)

// ResponseWriter records whether the response has started, the one piece of
// writer state the provider handlers relied on beyond net/http.
type ResponseWriter struct {
	http.ResponseWriter
	written bool
}

func (w *ResponseWriter) WriteHeader(status int) {
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *ResponseWriter) Write(p []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(p)
}

// Written reports whether any response bytes or a status line were sent. Once
// true, the request can no longer be retried on another channel.
func (w *ResponseWriter) Written() bool {
	return w.written
}

// Flush exposes streaming flushes to handlers through the wrapper.
func (w *ResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Renderer is the response rendering contract used by Render. common.CustomEvent
// implements it; the framework-independent form keeps handlers free of Gin.
type Renderer interface {
	Render(w http.ResponseWriter) error
}

// Context carries the HTTP transport values a provider adapter needs: the
// inbound request, the outbound writer, and per-request values set by
// converters. It replaces the Gin context in the Adaptor interface.
type Context struct {
	Request *http.Request
	Writer  *ResponseWriter

	values map[string]any
}

func NewContext(request *http.Request, writer http.ResponseWriter) *Context {
	return &Context{Request: request, Writer: &ResponseWriter{ResponseWriter: writer}}
}

func (c *Context) Set(key string, value any) {
	if c.values == nil {
		c.values = make(map[string]any)
	}
	c.values[key] = value
}

func (c *Context) Get(key string) (any, bool) {
	value, ok := c.values[key]
	return value, ok
}

// GetString returns the value for the key when it is a string, mirroring the
// context accessor the converters previously used. Channel metadata such as
// ctxkey.RequestModel is populated from the selected channel configuration.
func (c *Context) GetString(key string) string {
	if value, ok := c.values[key].(string); ok {
		return value
	}
	return ""
}

func (c *Context) GetHeader(key string) string {
	return c.Request.Header.Get(key)
}

// JSON writes an application/json response with the status code.
func (c *Context) JSON(status int, object any) {
	header := c.Writer.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json; charset=utf-8")
	}
	c.Writer.WriteHeader(status)
	encoded, err := json.Marshal(object)
	if err != nil {
		_, _ = c.Writer.Write([]byte(`{"error":{"message":"failed to encode response","type":"one_api_error"}}`))
		return
	}
	_, _ = c.Writer.Write(encoded)
}

// Render writes a prepared event payload. A negative code skips the status
// line so mid-stream events keep the already-sent headers.
func (c *Context) Render(code int, render Renderer) {
	if code > 0 {
		c.Writer.WriteHeader(code)
	}
	_ = render.Render(c.Writer)
}

// Stream invokes step with the writer until it returns false, flushing after
// every event so SSE frames reach the client immediately.
func (c *Context) Stream(step func(w io.Writer) bool) bool {
	for {
		if !step(c.Writer) {
			return false
		}
		c.Writer.Flush()
	}
}
