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
		`,"response_format":{"type":"json_object"}`,
		`,"stop":["1","2","3","4","5","6"]`,
		`,"tools":[{"type":"custom","custom":{"name":"search"}}]`,
		`,"tools":[{"type":"function","function":{"name":"search","strict":true}}]`,
		`,"tools":[{"type":"function","function":{"name":"search","parameters":"broken"}}]`,
		`,"tool_choice":{"type":"server_tool"}`,
	} {
		body := `{"model":"alias","messages":[{"role":"user","content":"hi"}]` + suffix + `}`
		if _, err := ChatToGemini([]byte(body), "gemini"); err == nil {
			t.Fatalf("lossy Chat request was accepted: %s", body)
		}
	}
	for _, message := range []string{
		`{"role":"developer","content":"rule"}`,
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}`,
		`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"ogg"}}]}`,
		`{"role":"assistant","content":"hi","tool_call_id":"call_1"}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"custom","custom":{"name":"search"}}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"search","arguments":"not-json"}}]}`,
		`{"role":"tool","tool_call_id":"unknown","content":"result"}`,
		`{"role":"tool","tool_call_id":"call_1","content":{"structured":true}}`,
	} {
		body := `{"model":"alias","messages":[` + message + `]}`
		if _, err := ChatToGemini([]byte(body), "gemini"); err == nil {
			t.Fatalf("lossy Chat message was accepted: %s", body)
		}
	}
}

func TestChatToGeminiConvertsToolsAndResults(t *testing.T) {
	input := []byte(`{"model":"alias","messages":[{"role":"user","content":"find it"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"test\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"{\"hits\":2}"},{"role":"user","content":"thanks"}],"tools":[{"type":"function","function":{"name":"search","description":"Search","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"search"}},"max_tokens":64}`)
	converted, err := ChatToGemini(input, "gemini-upstream")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tools []struct {
			Declarations []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode                string   `json:"mode"`
				AllowedFunctionList []string `json:"allowedFunctionNames"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             string          `json:"text"`
				FunctionCall     json.RawMessage `json:"functionCall"`
				FunctionResponse struct {
					ID       string         `json:"id"`
					Name     string         `json:"name"`
					Response map[string]any `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 || len(result.Tools[0].Declarations) != 1 || result.Tools[0].Declarations[0].Name != "search" || result.Tools[0].Declarations[0].Description != "Search" || string(result.Tools[0].Declarations[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("tool declarations were lost: %s", converted)
	}
	if result.ToolConfig.FunctionCallingConfig.Mode != "ANY" || len(result.ToolConfig.FunctionCallingConfig.AllowedFunctionList) != 1 || result.ToolConfig.FunctionCallingConfig.AllowedFunctionList[0] != "search" {
		t.Fatalf("tool choice was lost: %s", converted)
	}
	if len(result.Contents) != 3 {
		t.Fatalf("contents were not merged or converted: %s", converted)
	}
	var call struct {
		ID   string         `json:"id"`
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(result.Contents[1].Parts[0].FunctionCall, &call); err != nil {
		t.Fatal(err)
	}
	if result.Contents[1].Role != "model" || call.ID != "call_1" || call.Name != "search" || call.Args["q"] != "test" {
		t.Fatalf("tool call conversion failed: %s", converted)
	}
	response := result.Contents[2].Parts[0].FunctionResponse
	if result.Contents[2].Role != "user" || response.ID != "call_1" || response.Name != "search" || response.Response["hits"] != float64(2) {
		t.Fatalf("tool result conversion failed: %s", converted)
	}
	if result.Contents[2].Parts[1].Text != "thanks" {
		t.Fatalf("trailing user message was lost: %s", converted)
	}
}

func TestChatToGeminiConvertsTextToolResults(t *testing.T) {
	input := []byte(`{"model":"alias","messages":[{"role":"user","content":"find it"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"plain result"}]}`)
	converted, err := ChatToGemini(input, "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(converted), `"functionResponse":{"id":"call_1","name":"search","response":{"result":"plain result"}}`) {
		t.Fatalf("text tool result was not wrapped: %s", converted)
	}
}

func TestChatToGeminiConvertsImageAndAudioParts(t *testing.T) {
	input := []byte(`{"model":"alias","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}]}]}`)
	converted, err := ChatToGemini(input, "gemini")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"inlineData":{"data":"aGVsbG8=","mimeType":"image/png"}`, `"inlineData":{"data":"aGVsbG8=","mimeType":"audio/wav"}`} {
		if !strings.Contains(string(converted), want) {
			t.Fatalf("media part was not converted (%s): %s", want, converted)
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
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"secret","thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"ok","thoughtSignature":"c2ln"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"inlineData":{"data":"aGVsbG8=","mimeType":"image/png"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"fileData":{"fileUri":"gs://bucket/a.png"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"functionResponse":{"name":"search","response":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
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

func TestGeminiToChatConvertsFunctionCalls(t *testing.T) {
	input := []byte(`{"responseId":"resp_tool","candidates":[{"content":{"role":"model","parts":[{"text":"Searching"},{"functionCall":{"id":"gem-call-1","name":"search","args":{"q":"test"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"thoughtsTokenCount":2,"totalTokenCount":15}}`)
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
				Content   any `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(converted, &result); err != nil {
		t.Fatal(err)
	}
	choice := result.Choices[0]
	if result.ID != "resp_tool" || result.Model != "client-alias" || choice.FinishReason != "tool_calls" || choice.Message.Content != "Searching" || len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("function call conversion failed: %s", converted)
	}
	call := choice.Message.ToolCalls[0]
	if call.ID != "gem-call-1" || call.Type != "function" || call.Function.Name != "search" || call.Function.Arguments != `{"q":"test"}` {
		t.Fatalf("tool call fields were lost: %s", converted)
	}
	if usage.Input != 9 || usage.Output != 6 || !usage.Complete {
		t.Fatalf("usage is wrong: %+v", usage)
	}
}

func TestGeminiToChatGeneratesToolCallID(t *testing.T) {
	input := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}`)
	converted, _, err := GeminiToChat(input, "alias")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(converted), `"id":"call_`) || !strings.Contains(string(converted), `"finish_reason":"tool_calls"`) || strings.Contains(string(converted), `"content":"`) {
		t.Fatalf("tool-only response was not converted with a generated id: %s", converted)
	}
}
