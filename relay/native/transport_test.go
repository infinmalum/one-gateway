package native

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAnthropicRequestPreservesExtensionsAndReplacesCredential(t *testing.T) {
	original := []byte(`{"model":"client-model","max_tokens":20,"stream":true,"thinking":{"type":"enabled","budget_tokens":100},"future_field":{"nested":true}}`)
	request, err := BuildRequest(context.Background(), Request{
		Protocol: Anthropic,
		BaseURL:  "https://example.com/proxy",
		Model:    "upstream-model",
		APIKey:   "upstream-secret",
		Body:     original,
		Headers: http.Header{
			"Anthropic-Version": {"2023-06-01"},
			"Anthropic-Beta":    {"new-feature"},
			"Authorization":     {"Bearer gateway-secret"},
			"X-Api-Key":         {"gateway-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := request.URL.String(); got != "https://example.com/proxy/v1/messages" {
		t.Fatalf("unexpected URL: %s", got)
	}
	if request.Header.Get("x-api-key") != "upstream-secret" || request.Header.Get("Authorization") != "" || request.Header.Get("anthropic-beta") != "new-feature" {
		t.Fatal("request headers did not replace the gateway credential")
	}
	body, _ := io.ReadAll(request.Body)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["model"]) != `"upstream-model"` || string(got["future_field"]) != `{"nested":true}` {
		t.Fatalf("model mapping lost an extension: %s", body)
	}
}

func TestAnthropicUnmappedRequestKeepsExactBody(t *testing.T) {
	body := []byte("{\n  \"model\": \"same\", \"stream\": false, \"new\": 1\n}")
	request, err := BuildRequest(context.Background(), Request{Protocol: Anthropic, BaseURL: "https://example.com", Model: "same", APIKey: "key", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(request.Body)
	if !bytes.Equal(body, forwarded) {
		t.Fatalf("request was changed: %s", forwarded)
	}
}

func TestGeminiRequestPathAndAuth(t *testing.T) {
	request, err := BuildRequest(context.Background(), Request{
		Protocol: Gemini, BaseURL: "https://example.com", Version: "v1beta", Model: "gemini-future-preview",
		Action: "streamGenerateContent", APIKey: "provider-key", Body: []byte(`{"contents":[],"newField":42}`),
		Headers: http.Header{"X-Goog-Api-Key": {"gateway-secret"}},
		Query:   url.Values{"key": {"gateway-secret"}, "custom": {"kept"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := request.URL.String(); got != "https://example.com/v1beta/models/gemini-future-preview:streamGenerateContent?alt=sse&custom=kept" {
		t.Fatalf("unexpected URL: %s", got)
	}
	if request.Header.Get("x-goog-api-key") != "provider-key" || request.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("incorrect provider headers: %v", request.Header)
	}
}

func TestCopyAnthropicStreamPreservesNamedEventsAndCountsUsage(t *testing.T) {
	events := "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":2}}}\r\n\r\n" +
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"secret\"}}\r\n\r\n" +
		"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\r\n\r\n"
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(events), Anthropic, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != events || !usage.Seen || usage.Input != 7 || usage.Output != 9 {
		t.Fatalf("stream or usage changed: %+v %q", usage, w.Body.String())
	}
}

func TestCopyGeminiStreamPreservesUnknownFields(t *testing.T) {
	events := "data: {\"candidates\":[],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4,\"thoughtsTokenCount\":2,\"totalTokenCount\":10},\"future\":true}\n\n"
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(events), Gemini, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != events || usage.Input != 3 || usage.Output != 7 {
		t.Fatalf("stream or usage changed: %+v %q", usage, w.Body.String())
	}
}
