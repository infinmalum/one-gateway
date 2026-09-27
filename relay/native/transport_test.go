package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestNativeSystemPromptRewritesOnlyProtocolField(t *testing.T) {
	tests := []struct {
		name     string
		request  Request
		field    string
		expected string
	}{
		{
			name:    "Anthropic",
			request: Request{Protocol: Anthropic, BaseURL: "https://example.com", Model: "claude", APIKey: "key", SystemPrompt: "configured", Body: []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}],"system":[{"type":"text","text":"old"}],"future":{"keep":true}}`)},
			field:   "system", expected: `"configured"`,
		},
		{
			name:    "Gemini",
			request: Request{Protocol: Gemini, BaseURL: "https://example.com", Version: "v1", Model: "gemini-next", Action: "generateContent", APIKey: "key", SystemPrompt: "configured", Body: []byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}],"systemInstruction":{"parts":[{"text":"old"}]},"future":{"keep":true}}`)},
			field:   "systemInstruction", expected: `{"parts":[{"text":"configured"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := BuildRequest(context.Background(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields[test.field]) != test.expected || string(fields["future"]) != `{"keep":true}` {
				t.Fatalf("system prompt rewrite changed other fields: %s", body)
			}
		})
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

func TestOpenAIResponsesRequestAndEvents(t *testing.T) {
	body := []byte(`{"model":"alias","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}],"stream":true,"future":{"keep":true}}`)
	request, err := BuildRequest(context.Background(), Request{
		Protocol: OpenAIResponses, BaseURL: "https://example.com", Model: "upstream-model",
		APIKey: "provider-key", SystemPrompt: "configured", Body: body,
		Headers: http.Header{"Authorization": {"Bearer gateway-key"}, "Openai-Beta": {"future-feature"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer provider-key" || request.Header.Get("Accept") != "text/event-stream" || request.Header.Get("OpenAI-Beta") != "future-feature" {
		t.Fatalf("Responses route or headers are incorrect: %s %v", request.URL, request.Header)
	}
	forwarded, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(forwarded, &fields); err != nil || string(fields["model"]) != `"upstream-model"` || string(fields["instructions"]) != `"configured"` || string(fields["future"]) != `{"keep":true}` || !bytes.Contains(fields["input"], []byte("input_image")) {
		t.Fatalf("Responses request lost fields: %s %v", forwarded, err)
	}
	events := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"future\":true}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":11,\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n"
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(events), OpenAIResponses, true)
	if err != nil || w.Body.String() != events || !usage.Seen || !usage.Complete || usage.Input != 7 || usage.Output != 11 {
		t.Fatalf("Responses stream or usage changed: %+v %q %v", usage, w.Body.String(), err)
	}
}

func TestOpenAIResponsesUnmappedBodyAndNormalUsage(t *testing.T) {
	body := []byte("{\n  \"model\": \"same\", \"input\": \"hello\", \"future\": true\n}")
	request, err := BuildRequest(context.Background(), Request{Protocol: OpenAIResponses, BaseURL: "https://example.com", Model: "same", APIKey: "key", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(request.Body)
	if !bytes.Equal(forwarded, body) {
		t.Fatalf("unmapped Responses body changed: %s", forwarded)
	}
	response := `{"object":"response","status":"completed","usage":{"input_tokens":2,"output_tokens":3},"future":true}`
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(response), OpenAIResponses, false)
	if err != nil || w.Body.String() != response || usage.Input != 2 || usage.Output != 3 {
		t.Fatalf("normal Responses output or usage changed: %+v %q %v", usage, w.Body.String(), err)
	}
}

func TestOpenAIResponsesTerminalEventsKeepTheirUsage(t *testing.T) {
	for _, eventType := range []string{"response.failed", "response.incomplete"} {
		events := "event: " + eventType + "\ndata: {\"type\":\"" + eventType + "\",\"response\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n"
		w := httptest.NewRecorder()
		usage, err := CopyResponse(w, strings.NewReader(events), OpenAIResponses, true)
		if err != nil || w.Body.String() != events || !usage.Complete || !usage.Seen || usage.Input != 9 || usage.Output != 2 {
			t.Fatalf("%s lost the terminal event or usage: %+v %q %v", eventType, usage, w.Body.String(), err)
		}
	}
}

func TestOpenAIChatPreservesUnmappedBodyAndStreamUsage(t *testing.T) {
	body := []byte("{\n  \"model\": \"same\", \"messages\": [{\"role\":\"user\",\"content\":\"hello\"}], \"future\": true\n}")
	request, err := BuildRequest(context.Background(), Request{Protocol: OpenAIChat, BaseURL: "https://example.com", Model: "same", APIKey: "provider-key", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(request.Body)
	if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer provider-key" || !bytes.Equal(body, forwarded) {
		t.Fatalf("unmapped OpenAI Chat request changed: %s %v", forwarded, request.Header)
	}
	events := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5}}\n\n" + "data: [DONE]\n\n"
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(events), OpenAIChat, true)
	if err != nil || w.Body.String() != events || !usage.Seen || !usage.Complete || usage.Input != 3 || usage.Output != 5 {
		t.Fatalf("OpenAI Chat stream or usage changed: %+v %q %v", usage, w.Body.String(), err)
	}
}

func TestOpenAIChatSystemPromptPreservesOtherFields(t *testing.T) {
	body := []byte(`{"model":"alias","messages":[{"role":"system","content":"old","future":true},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"search"}}],"future":{"keep":true}}`)
	request, err := BuildRequest(context.Background(), Request{Protocol: OpenAIChat, BaseURL: "https://example.com", Model: "upstream", APIKey: "key", SystemPrompt: "configured", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(request.Body)
	var fields struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Future  bool   `json:"future"`
		} `json:"messages"`
		Tools  json.RawMessage `json:"tools"`
		Future json.RawMessage `json:"future"`
	}
	if err := json.Unmarshal(forwarded, &fields); err != nil || fields.Model != "upstream" || len(fields.Messages) != 2 || fields.Messages[0].Content != "configured" || !fields.Messages[0].Future || len(fields.Tools) == 0 || string(fields.Future) != `{"keep":true}` {
		t.Fatalf("OpenAI Chat system prompt changed unrelated fields: %s %v", forwarded, err)
	}
}

func TestOpenAIEmbeddingsPreserveFieldsAndLargeUsage(t *testing.T) {
	body := []byte(`{"model":"alias","input":["first","second"],"dimensions":256,"encoding_format":"base64","future":{"keep":true}}`)
	unmapped, err := BuildRequest(context.Background(), Request{Protocol: OpenAIEmbeddings, BaseURL: "https://example.com", Model: "alias", APIKey: "provider-key", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	unmappedBody, _ := io.ReadAll(unmapped.Body)
	if !bytes.Equal(unmappedBody, body) {
		t.Fatalf("unmapped embedding request changed: %s", unmappedBody)
	}
	request, err := BuildRequest(context.Background(), Request{Protocol: OpenAIEmbeddings, BaseURL: "https://example.com", Model: "upstream", APIKey: "provider-key", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(request.Body)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(forwarded, &fields); err != nil || request.URL.Path != "/v1/embeddings" || request.Header.Get("Authorization") != "Bearer provider-key" || string(fields["model"]) != `"upstream"` || string(fields["input"]) != `["first","second"]` || string(fields["future"]) != `{"keep":true}` {
		t.Fatalf("embedding request changed: %s %v", forwarded, err)
	}
	large := `{"object":"list","data":[{"embedding":[` + strings.Repeat("0.123456,", 300000) + `0.123456]}],"usage":{"prompt_tokens":13,"total_tokens":13}}`
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(large), OpenAIEmbeddings, false)
	if err != nil || w.Body.String() != large || !usage.Seen || usage.Input != 13 || usage.Output != 0 {
		t.Fatalf("large embedding response lost usage: %+v len=%d err=%v", usage, w.Body.Len(), err)
	}
}

func TestOpenAICompletionsPreserveBodyAndStream(t *testing.T) {
	body := []byte("{\n  \"model\": \"same\", \"prompt\": \"hello\", \"stream\": true, \"logprobs\": 3, \"future\": true\n}")
	request, err := BuildRequest(context.Background(), Request{Protocol: OpenAICompletions, BaseURL: "https://example.com", Model: "same", APIKey: "provider-key", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(request.Body)
	if !bytes.Equal(forwarded, body) || request.URL.Path != "/v1/completions" || request.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("unmapped Completions request changed: %s %v", forwarded, request.Header)
	}
	events := "data: {\"choices\":[{\"text\":\"hello\"}]}\n\n" + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\n" + "data: [DONE]\n\n"
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, strings.NewReader(events), OpenAICompletions, true)
	if err != nil || w.Body.String() != events || !usage.Seen || !usage.Complete || usage.Input != 2 || usage.Output != 3 {
		t.Fatalf("Completions stream or usage changed: %+v %q %v", usage, w.Body.String(), err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("upstream interrupted") }

func TestNonStreamingCopyReportsUpstreamReadFailure(t *testing.T) {
	w := httptest.NewRecorder()
	usage, err := CopyResponse(w, io.MultiReader(strings.NewReader(`{"data":[`), brokenReader{}), OpenAIEmbeddings, false)
	if err == nil || usage.Complete || w.Body.String() != `{"data":[` {
		t.Fatalf("read failure was hidden: usage %+v body %q err %v", usage, w.Body.String(), err)
	}
}
