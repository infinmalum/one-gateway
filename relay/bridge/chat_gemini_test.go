package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatToGeminiTextConversation(t *testing.T) {
	input := []byte(`{"model":"client-alias","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":" world"}]},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}],"max_completion_tokens":32,"temperature":0.4,"top_p":0.8,"stop":["END"]}`)
	converted, err := ChatToGemini(input, "gemini-upstream")
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
			Temperature     float64  `json:"temperature"`
			TopP            float64  `json:"topP"`
			StopSequences   []string `json:"stopSequences"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.SystemInstruction.Parts[0].Text != "Be concise" || len(result.Contents) != 3 || result.Contents[0].Role != "user" || len(result.Contents[0].Parts) != 2 || result.Contents[1].Role != "model" || result.Contents[2].Parts[0].Text != "again" || result.GenerationConfig.MaxOutputTokens != 32 || result.GenerationConfig.Temperature != 0.4 || result.GenerationConfig.TopP != 0.8 || result.GenerationConfig.StopSequences[0] != "END" {
		t.Fatalf("Chat to Gemini lost fields: %s", converted)
	}
}

func TestChatToGeminiRejectsLossyRequests(t *testing.T) {
	for _, suffix := range []string{
		`,"tools":[{"type":"function","function":{"name":"search"}}]`,
		`,"response_format":{"type":"json_object"}`,
		`,"stop":["1","2","3","4","5","6"]`,
	} {
		body := `{"model":"alias","messages":[{"role":"user","content":"hi"}]` + suffix + `}`
		if _, err := ChatToGemini([]byte(body), "gemini"); err == nil {
			t.Fatalf("lossy Chat request was accepted: %s", body)
		}
	}
	for _, message := range []string{
		`{"role":"developer","content":"rule"}`,
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"search","arguments":"{}"}}]}`,
	} {
		body := `{"model":"alias","messages":[` + message + `]}`
		if _, err := ChatToGemini([]byte(body), "gemini"); err == nil {
			t.Fatalf("lossy Chat message was accepted: %s", body)
		}
	}
}

func TestGeminiToChatPreservesUsageAndMetadata(t *testing.T) {
	input := []byte(`{"responseId":"resp_1","modelVersion":"gemini-upstream","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"hello"},{"text":" world"}]},"finishReason":"STOP","safetyRatings":[{"category":"HARM_CATEGORY_HATE_SPEECH","probability":"NEGLIGIBLE"}]}],"usageMetadata":{"promptTokenCount":7,"cachedContentTokenCount":2,"candidatesTokenCount":3,"thoughtsTokenCount":4,"totalTokenCount":14}}`)
	converted, usage, err := GeminiToChat(input, "client-alias")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
			Details    struct {
				Cached int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		ProviderMetadata struct {
			Gemini struct {
				ModelVersion  string          `json:"modelVersion"`
				SafetyRatings json.RawMessage `json:"safetyRatings"`
			} `json:"gemini"`
		} `json:"provider_metadata"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "resp_1" || result.Model != "client-alias" || len(result.Choices) != 1 || result.Choices[0].FinishReason != "stop" || result.Choices[0].Message.Content != "hello world" || result.Usage.Prompt != 7 || result.Usage.Completion != 7 || result.Usage.Details.Cached != 2 || result.ProviderMetadata.Gemini.ModelVersion != "gemini-upstream" || !strings.Contains(string(result.ProviderMetadata.Gemini.SafetyRatings), "HARM_CATEGORY_HATE_SPEECH") || !usage.Seen || usage.Input != 7 || usage.Output != 7 {
		t.Fatalf("Gemini response lost fields: %s usage %+v", converted, usage)
	}
}

func TestGeminiToChatGeneratesIDWhenUpstreamOmitsOne(t *testing.T) {
	input := []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}`)
	converted, usage, err := GeminiToChat(input, "alias")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(converted), `"id":"chatcmpl_`) || !strings.Contains(string(converted), `"finish_reason":"length"`) || usage.Input != 2 || usage.Output != 1 {
		t.Fatalf("Gemini fallback ID or finish reason failed: %s usage %+v", converted, usage)
	}
}

func TestGeminiToChatRejectsUnrepresentableResponses(t *testing.T) {
	for _, body := range []string{
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"secret","thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"blocked"}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[],"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","newField":true}],"usageMetadata":{"promptTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"index":1,"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{}}`,
	} {
		if _, _, err := GeminiToChat([]byte(body), "alias"); err == nil {
			t.Fatalf("lossy Gemini response was accepted: %s", body)
		}
	}
}
