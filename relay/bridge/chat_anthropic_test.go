package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatToAnthropicPreservesToolsAndConversation(t *testing.T) {
	input := []byte(`{"model":"alias","max_completion_tokens":24,"messages":[{"role":"system","content":"system"},{"role":"developer","content":"developer"},{"role":"user","content":[{"type":"text","text":"find it"}]},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"test\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"found"}],"tools":[{"type":"function","function":{"name":"search","description":"Search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],"tool_choice":"required","stop":["done"]}`)
	converted, err := ChatToAnthropic(input, "claude-upstream")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
		System    string `json:"system"`
		Messages  []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		Tools      []map[string]any `json:"tools"`
		ToolChoice map[string]any   `json:"tool_choice"`
		Stop       []string         `json:"stop_sequences"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.Model != "claude-upstream" || result.MaxTokens != 24 || result.System != "system\n\ndeveloper" || len(result.Messages) != 3 || result.Messages[1].Content[0]["type"] != "tool_use" || result.Messages[2].Content[0]["tool_use_id"] != "call_1" || result.ToolChoice["type"] != "any" || len(result.Tools) != 1 || result.Stop[0] != "done" {
		t.Fatalf("Chat request conversion lost fields: %s", converted)
	}
}

func TestChatToAnthropicRejectsLossyFeatures(t *testing.T) {
	for _, body := range []string{
		`{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		`{"model":"alias","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`,
		`{"model":"alias","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
		`{"model":"alias","messages":[{"role":"user","content":"hi"}],"n":2}`,
	} {
		if _, err := ChatToAnthropic([]byte(body), "claude-upstream"); err == nil {
			t.Fatalf("lossy Chat request was accepted: %s", body)
		}
	}
}

func TestAnthropicToChatConvertsToolsUsageAndRejectsThinking(t *testing.T) {
	input := []byte(`{"id":"msg_1","type":"message","stop_reason":"tool_use","content":[{"type":"text","text":"Searching"},{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"test"}}],"usage":{"input_tokens":3,"cache_read_input_tokens":2,"output_tokens":7}}`)
	converted, usage, err := AnthropicToChat(input, "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.Model != "client-alias" || result.Choices[0].FinishReason != "tool_calls" || result.Choices[0].Message.Content != "Searching" || result.Choices[0].Message.ToolCalls[0].ID != "toolu_1" || result.Choices[0].Message.ToolCalls[0].Function.Name != "search" || !strings.Contains(result.Choices[0].Message.ToolCalls[0].Function.Arguments, `"q":"test"`) || !usage.Seen || usage.Input != 5 || usage.Output != 7 {
		t.Fatalf("Anthropic response conversion lost fields: %s usage %+v", converted, usage)
	}
	if _, _, err := AnthropicToChat([]byte(`{"id":"msg_2","type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"private"}]}`), "alias"); err == nil {
		t.Fatal("thinking block was silently dropped")
	}
}
