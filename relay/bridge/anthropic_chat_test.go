package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicToChatRequestPreservesConversationAndTools(t *testing.T) {
	input := []byte(`{"model":"client-model","max_tokens":32,"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":[{"type":"text","text":"find it"}]},{"role":"assistant","content":[{"type":"text","text":"Searching"},{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"test"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"found"}]}],"tools":[{"name":"search","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"search"},"stop_sequences":["done"]}`)
	converted, err := AnthropicToChatRequest(input, "upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_completion_tokens"`
		Messages  []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools      []any `json:"tools"`
		ToolChoice struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_choice"`
		Stop []string `json:"stop"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.Model != "upstream-model" || result.MaxTokens != 32 || len(result.Messages) != 4 || result.Messages[0].Role != "system" || string(result.Messages[0].Content) != `"system"` || result.Messages[2].Role != "assistant" || !strings.Contains(result.Messages[2].ToolCalls[0].Function.Arguments, `"q":"test"`) || result.Messages[3].Role != "tool" || result.Messages[3].ToolCallID != "toolu_1" || len(result.Tools) != 1 || result.ToolChoice.Function.Name != "search" || result.Stop[0] != "done" {
		t.Fatalf("Messages conversion lost fields: %s", converted)
	}
}

func TestAnthropicToChatStreamingRequestsUsage(t *testing.T) {
	converted, err := AnthropicToChatRequest([]byte(`{"model":"alias","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`), "upstream")
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Stream        bool `json:"stream"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(converted, &request); err != nil || !request.Stream || !request.StreamOptions.IncludeUsage {
		t.Fatalf("converted streaming Chat request omitted usage: %s err %v", converted, err)
	}
}

func TestAnthropicToChatResponsePreservesUsageAndRejectsLossyFeatures(t *testing.T) {
	input := []byte(`{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"Searching","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"test\"}"}}]}}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":2}}}`)
	converted, usage, err := ChatToAnthropicResponse(input, "client-model")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Model   string `json:"model"`
		Stop    string `json:"stop_reason"`
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"content"`
		Usage struct {
			Input     int64 `json:"input_tokens"`
			CacheRead int64 `json:"cache_read_input_tokens"`
			Output    int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.Model != "client-model" || result.Stop != "tool_use" || len(result.Content) != 2 || result.Content[1].ID != "call_1" || result.Usage.Input != 6 || result.Usage.CacheRead != 2 || result.Usage.Output != 3 || !usage.Seen || usage.Input != 8 || usage.Output != 3 {
		t.Fatalf("Chat response conversion lost fields: %s usage %+v", converted, usage)
	}
	for _, body := range []string{
		`{"model":"x","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!!!"}}]}],"max_tokens":10}`,
		`{"model":"x","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"thinking":{"type":"enabled","budget_tokens":8}}`,
		`{"model":"x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok","is_error":true}]}],"max_tokens":10}`,
	} {
		if _, err := AnthropicToChatRequest([]byte(body), "upstream"); err == nil {
			t.Fatalf("lossy Messages request was accepted: %s", body)
		}
	}
	if _, _, err := ChatToAnthropicResponse([]byte(strings.Replace(string(input), `"finish_reason":"tool_calls"`, `"finish_reason":"content_filter"`, 1)), "client-model"); err == nil {
		t.Fatal("content filter was converted to a refusal")
	}
	if _, _, err := ChatToAnthropicResponse([]byte(strings.Replace(string(input), `,"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":2}}`, ``, 1)), "client-model"); err == nil {
		t.Fatal("Chat response without usage produced an invalid Messages response")
	}
}
