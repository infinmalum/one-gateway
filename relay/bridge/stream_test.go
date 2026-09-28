package bridge

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/infinmalum/one-gateway/relay/native"
)

type recordingWriter struct {
	buffer  bytes.Buffer
	flushes int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	return w.buffer.Write(p)
}

func (w *recordingWriter) Flush() {
	w.flushes++
}

func (w *recordingWriter) events(t *testing.T) []string {
	t.Helper()
	var result []string
	for _, line := range strings.Split(w.buffer.String(), "\n") {
		if strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "data:{") {
			result = append(result, strings.TrimPrefix(strings.TrimPrefix(line, "data: "), "data:"))
		}
	}
	return result
}

func anthropicStreamFixture() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-upstream\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":2}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" there\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"search\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"x\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func TestAnthropicStreamToChatChunks(t *testing.T) {
	writer := &recordingWriter{}
	usage, err := chatAnthropic{}.Stream(writer, strings.NewReader(anthropicStreamFixture()), "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	events := writer.events(t)
	if len(events) < 6 {
		t.Fatalf("expected converted chunks, got %d: %v", len(events), events)
	}
	var first struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(events[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.ID != "msg_stream" || first.Object != "chat.completion.chunk" || first.Model != "client-alias" || first.Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk is wrong: %s", events[0])
	}
	var text string
	var toolArguments string
	toolSeen := false
	for _, raw := range events {
		if raw == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("chunk is not valid JSON: %s", raw)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		text += chunk.Choices[0].Delta.Content
		for _, call := range chunk.Choices[0].Delta.ToolCalls {
			if call.ID != "" {
				toolSeen = true
			}
			toolArguments += call.Function.Arguments
		}
	}
	if text != "hello there" || !toolSeen || toolArguments != `{"q":"x"}` {
		t.Fatalf("stream conversion lost content: text %q arguments %q", text, toolArguments)
	}
	last := events[len(events)-1]
	if last != "[DONE]" {
		t.Fatalf("stream did not end with [DONE]: %s", last)
	}
	if !usage.Seen || usage.Input != 5 || usage.Output != 7 || !usage.Complete {
		t.Fatalf("stream usage is wrong: %+v", usage)
	}
	if writer.flushes == 0 {
		t.Fatal("converted stream was not flushed")
	}
}

func TestAnthropicStreamRejectsThinking(t *testing.T) {
	fixture := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"private\"}}\n\n"
	writer := &recordingWriter{}
	usage, err := chatAnthropic{}.Stream(writer, strings.NewReader(fixture), "alias")
	if err == nil || !strings.Contains(err.Error(), "cannot be converted") {
		t.Fatalf("thinking delta was accepted: %v", err)
	}
	if !strings.Contains(writer.buffer.String(), "gateway_stream_error") || strings.Contains(writer.buffer.String(), "[DONE]") {
		t.Fatalf("conversion failure was not surfaced to the client: %s", writer.buffer.String())
	}
	if usage.Complete {
		t.Fatal("failed stream claimed completion")
	}
}

func TestAnthropicStreamWithoutStopFails(t *testing.T) {
	fixture := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":1}}}\n\n"
	writer := &recordingWriter{}
	if _, err := (chatAnthropic{}).Stream(writer, strings.NewReader(fixture), "alias"); err == nil || !strings.Contains(err.Error(), "message_stop") {
		t.Fatalf("truncated Anthropic stream was accepted: %v", err)
	}
}

func chatStreamFixture() string {
	return "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"system_fingerprint\":\"fp_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"q\\\":1}\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":9}}\n\n" +
		"data: [DONE]\n\n"
}

func TestChatStreamToAnthropicEvents(t *testing.T) {
	writer := &recordingWriter{}
	usage, err := anthropicChat{}.Stream(writer, strings.NewReader(chatStreamFixture()), "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	body := writer.buffer.String()
	for _, want := range []string{
		"event: message_start",
		`"content_block":{"text":"","type":"text"}`,
		`"text":"hello"`,
		`"text_delta"`,
		`"id":"call_1"`,
		`"name":"search"`,
		`"input_json_delta"`,
		`"partial_json":"{\"q\":1}"`,
		`"content_block_stop"`,
		`"stop_reason":"tool_use"`,
		`"output_tokens":9`,
		"event: message_stop",
		`"provider_metadata":{"openai":{"system_fingerprint":"fp_1"}}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Anthropic stream is missing %q: %s", want, body)
		}
	}
	if !usage.Seen || usage.Input != 4 || usage.Output != 9 || !usage.Complete {
		t.Fatalf("stream usage is wrong: %+v", usage)
	}
	var messageStart struct {
		Message struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens int64 `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	for _, event := range writer.events(t) {
		if strings.Contains(event, "message_start") {
			if err := json.Unmarshal([]byte(event), &messageStart); err != nil {
				t.Fatal(err)
			}
		}
	}
	if messageStart.Message.Model != "client-alias" {
		t.Fatalf("message_start lost the requested model: %s", body)
	}
}

func TestChatStreamWithoutDoneFails(t *testing.T) {
	fixture := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
	writer := &recordingWriter{}
	if _, err := (anthropicChat{}).Stream(writer, strings.NewReader(fixture), "alias"); err == nil || !strings.Contains(err.Error(), "[DONE]") {
		t.Fatalf("truncated Chat stream was accepted: %v", err)
	}
	if strings.Contains(writer.buffer.String(), "message_stop") {
		t.Fatal("truncated Chat stream produced a message_stop event")
	}
}

func TestChatStreamWithoutFinishReasonFails(t *testing.T) {
	fixture := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
	writer := &recordingWriter{}
	if _, err := (anthropicChat{}).Stream(writer, strings.NewReader(fixture), "alias"); err == nil || !strings.Contains(err.Error(), "finish reason") {
		t.Fatalf("Chat stream without a finish reason was accepted: %v", err)
	}
	if strings.Contains(writer.buffer.String(), "message_stop") {
		t.Fatal("stream without a finish reason produced a message_stop event")
	}
}

func TestToolOnlyChatStreamStartsAnthropicMessageAndSettlesUsage(t *testing.T) {
	fixture := "data: {\"id\":\"chatcmpl-tool\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"\"}}]},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"id\":\"chatcmpl-tool\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":null}\n\n" +
		"data: {\"id\":\"chatcmpl-tool\",\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	writer := &recordingWriter{}
	usage, err := (anthropicChat{}).Stream(writer, strings.NewReader(fixture), "alias")
	if err != nil {
		t.Fatal(err)
	}
	events := writer.events(t)
	if len(events) < 4 || !strings.Contains(events[0], `"type":"message_start"`) || !strings.Contains(events[1], `"type":"content_block_start"`) || !strings.Contains(events[1], `"tool_use"`) || !strings.Contains(writer.buffer.String(), `"input_tokens":8`) || !strings.Contains(writer.buffer.String(), `"output_tokens":3`) || !usage.Complete || usage.Input != 8 || usage.Output != 3 {
		t.Fatalf("tool-only stream or usage was invalid: %s usage %+v", writer.buffer.String(), usage)
	}
}

func TestChatStreamWithoutUsageDoesNotFinishMessages(t *testing.T) {
	for _, usageChunk := range []string{"", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8}}\n\n"} {
		fixture := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
			usageChunk + "data: [DONE]\n\n"
		writer := &recordingWriter{}
		usage, err := (anthropicChat{}).Stream(writer, strings.NewReader(fixture), "alias")
		if err == nil || !strings.Contains(err.Error(), "provider usage") || usage.Complete || strings.Contains(writer.buffer.String(), "message_stop") {
			t.Fatalf("stream without complete usage looked complete: %v %s", err, writer.buffer.String())
		}
	}
}

func TestAnthropicStreamRequiresFinishReason(t *testing.T) {
	fixture := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"content\":[],\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	writer := &recordingWriter{}
	usage, err := (chatAnthropic{}).Stream(writer, strings.NewReader(fixture), "alias")
	if err == nil || !strings.Contains(err.Error(), "finish reason") || usage.Complete || strings.Contains(writer.buffer.String(), "data: [DONE]") {
		t.Fatalf("Anthropic stream without finish looked complete: %v %s", err, writer.buffer.String())
	}
}

func TestAnthropicStreamKeepsInitialBlockText(t *testing.T) {
	fixture := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"content\":[],\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"first\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" second\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	writer := &recordingWriter{}
	if _, err := (chatAnthropic{}).Stream(writer, strings.NewReader(fixture), "alias"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.buffer.String(), `"content":"first"`) || !strings.Contains(writer.buffer.String(), `"content":" second"`) {
		t.Fatalf("initial Anthropic text was dropped: %s", writer.buffer.String())
	}
}

func TestGeminiStreamPreservesUnknownTopLevelFields(t *testing.T) {
	fixture := "data: {\"responseId\":\"r1\",\"future\":{\"flag\":true},\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}\n\n"
	for _, converter := range []StreamConverter{chatGemini{}, messagesGemini{}} {
		writer := &recordingWriter{}
		if _, err := converter.Stream(writer, strings.NewReader(fixture), "alias"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(writer.buffer.String(), `"future":{"flag":true}`) {
			t.Fatalf("Gemini extension was dropped: %s", writer.buffer.String())
		}
	}
}

func TestChatStreamRejectsUnknownDeltaFields(t *testing.T) {
	fixture := "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\",\"reasoning_content\":\"secret\"}}]}\n\ndata: [DONE]\n\n"
	writer := &recordingWriter{}
	if _, err := (anthropicChat{}).Stream(writer, strings.NewReader(fixture), "alias"); err == nil || !strings.Contains(err.Error(), "reasoning_content") {
		t.Fatalf("unknown delta field was not rejected: %v", err)
	}
}

func geminiStreamFixture() string {
	return "data: {\"responseId\":\"r1\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]},\"safetyRatings\":[]}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1},\"modelVersion\":\"gemini-up\"}\n\n" +
		"data: {\"responseId\":\"r1\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\",\"safetyRatings\":[]}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":1,\"totalTokenCount\":6}}\n\n"
}

func TestGeminiStreamToChatChunks(t *testing.T) {
	writer := &recordingWriter{}
	usage, err := chatGemini{}.Stream(writer, strings.NewReader(geminiStreamFixture()), "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	events := writer.events(t)
	if len(events) != 4 || events[len(events)-1] != "[DONE]" {
		t.Fatalf("Gemini chunk sequence is wrong: %v", events)
	}
	var text string
	var finish string
	for _, raw := range events[:3] {
		var chunk struct {
			ID     string `json:"id"`
			Choice []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
			} `json:"usage"`
			ProviderMetadata struct {
				Gemini struct {
					ModelVersion string `json:"modelVersion"`
				} `json:"gemini"`
			} `json:"provider_metadata"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.ID != "r1" {
			t.Fatalf("chunk lost the upstream response ID: %s", raw)
		}
		text += chunk.Choice[0].Delta.Content
		if chunk.Choice[0].FinishReason != "" {
			finish = chunk.Choice[0].FinishReason
		}
		if chunk.Choice[0].FinishReason != "" && (chunk.Usage.Prompt != 2 || chunk.Usage.Completion != 4 || chunk.ProviderMetadata.Gemini.ModelVersion != "gemini-up") {
			t.Fatalf("final chunk lost usage or metadata: %s", raw)
		}
	}
	if text != "hello" || finish != "stop" {
		t.Fatalf("Gemini stream conversion lost content: %q %q", text, finish)
	}
	if !usage.Seen || usage.Input != 2 || usage.Output != 4 || !usage.Complete {
		t.Fatalf("Gemini stream usage is wrong: %+v", usage)
	}
}

func TestGeminiStreamToAnthropicEvents(t *testing.T) {
	writer := &recordingWriter{}
	usage, err := messagesGemini{}.Stream(writer, strings.NewReader(geminiStreamFixture()), "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	body := writer.buffer.String()
	for _, want := range []string{
		"event: message_start",
		`"content_block_start"`,
		`"text":"hel"`,
		`"text":"lo"`,
		`"text_delta"`,
		`"stop_reason":"end_turn"`,
		`"output_tokens":4`,
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Anthropic stream is missing %q: %s", want, body)
		}
	}
	if !usage.Seen || usage.Input != 2 || usage.Output != 4 || !usage.Complete {
		t.Fatalf("Gemini stream usage is wrong: %+v", usage)
	}
}

func TestGeminiStreamRejectsUnsupportedParts(t *testing.T) {
	fixture := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"search\",\"args\":{}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1}}\n\n"
	writer := &recordingWriter{}
	if _, err := (chatGemini{}).Stream(writer, strings.NewReader(fixture), "alias"); err == nil || !strings.Contains(err.Error(), "cannot be converted") {
		t.Fatalf("function call part was accepted: %v", err)
	}
}

func TestGeminiStreamWithoutFinishReasonFails(t *testing.T) {
	fixture := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1}}\n\n"
	writer := &recordingWriter{}
	if _, err := (chatGemini{}).Stream(writer, strings.NewReader(fixture), "alias"); err == nil || !strings.Contains(err.Error(), "finish reason") {
		t.Fatalf("truncated Gemini stream was accepted: %v", err)
	}
}

func TestMessagesToGeminiTextConversation(t *testing.T) {
	input := []byte(`{"model":"alias","max_tokens":48,"system":[{"type":"text","text":"Be brief"}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}],"temperature":0.5,"top_k":20,"stop_sequences":["END"]}`)
	converted, err := MessagesToGemini(input, "gemini-upstream")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			MaxOutputTokens int64    `json:"maxOutputTokens"`
			TopK            float64  `json:"topK"`
			StopSequences   []string `json:"stopSequences"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.SystemInstruction.Parts[0].Text != "Be brief" || len(result.Contents) != 2 || result.Contents[0].Role != "user" || result.Contents[1].Role != "model" || result.GenerationConfig.MaxOutputTokens != 48 || result.GenerationConfig.TopK != 20 || result.GenerationConfig.StopSequences[0] != "END" {
		t.Fatalf("Messages to Gemini lost fields: %s", converted)
	}
}

func TestMessagesToGeminiRejectsLossyRequests(t *testing.T) {
	for _, body := range []string{
		`{"model":"alias","max_tokens":8,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`,
		`{"model":"alias","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"search","input_schema":{"type":"object"}}]}`,
		`{"model":"alias","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8}}`,
		`{"model":"alias","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stop_sequences":["1","2","3","4","5","6"]}`,
		`{"model":"alias","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		if _, err := MessagesToGemini([]byte(body), "gemini"); err == nil {
			t.Fatalf("lossy Messages request was accepted: %s", body)
		}
	}
}

func TestGeminiToMessagesPreservesUsageAndMetadata(t *testing.T) {
	input := []byte(`{"responseId":"resp_9","modelVersion":"gemini-upstream","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":"MAX_TOKENS","safetyRatings":[{"category":"HARM_CATEGORY_HARASSMENT","probability":"LOW"}]}],"usageMetadata":{"promptTokenCount":9,"cachedContentTokenCount":3,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":16}}`)
	converted, usage, err := GeminiToMessages(input, "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			Input    int64 `json:"input_tokens"`
			Cached   int64 `json:"cache_read_input_tokens"`
			Creation int64 `json:"cache_creation_input_tokens"`
			Output   int64 `json:"output_tokens"`
		} `json:"usage"`
		ProviderMetadata struct {
			Gemini struct {
				SafetyRatings json.RawMessage `json:"safetyRatings"`
			} `json:"gemini"`
		} `json:"provider_metadata"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "resp_9" || result.Type != "message" || result.Model != "client-alias" || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "answer" || result.StopReason != "max_tokens" || result.Usage.Input != 6 || result.Usage.Cached != 3 || result.Usage.Creation != 0 || result.Usage.Output != 7 || !strings.Contains(string(result.ProviderMetadata.Gemini.SafetyRatings), "HARASSMENT") {
		t.Fatalf("Gemini to Messages lost fields: %s", converted)
	}
	if !usage.Seen || usage.Input != 9 || usage.Output != 7 || !usage.Complete {
		t.Fatalf("usage is wrong: %+v", usage)
	}
}

func TestGeminiToMessagesRejectsUnrepresentableResponses(t *testing.T) {
	for _, body := range []string{
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"s","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"a"},{"text":"b"}]},"finishReason":"STOP","index":1}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"user","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1}}`,
	} {
		if _, _, err := GeminiToMessages([]byte(body), "alias"); err == nil {
			t.Fatalf("lossy Gemini response was accepted: %s", body)
		}
	}
}

func TestBridgeForWiresGeminiMessages(t *testing.T) {
	converter, err := For(native.Anthropic, native.Gemini)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := converter.Response([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`), "alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := For(native.Gemini, native.OpenAIChat); err == nil {
		t.Fatal("Gemini inbound conversion should not be supported yet")
	}
	if _, ok := converter.(StreamConverter); !ok {
		t.Fatal("Messages to Gemini converter does not support streaming")
	}
}

var _ = io.EOF
