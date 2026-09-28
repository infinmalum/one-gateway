package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestStreamedConversionsUseTheNativeLifecycle covers the streaming protocol
// conversions end to end: a streamed client request must be converted on the
// shared lifecycle, settle quota once with the provider usage, and never reach
// the legacy relay controller.
func TestStreamedConversionsUseTheNativeLifecycle(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := client.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:streamed-conversions?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	model.DB, model.LOG_DB = db, db
	client.HTTPClient = &http.Client{}
	common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes = false, false, 0
	common.UsingSQLite = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		client.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes = previousRedis, previousMemoryCache, previousRetries
		common.UsingSQLite = previousSQLite
		_ = sqlDB.Close()
	})
	user := model.User{Username: "stream-test", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "gateway-key", Name: "stream", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	anthropicEvents := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-streamed\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":4}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var anthropicCalls int
	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-stream-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("Anthropic upstream got incorrect route or credentials: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"claude-streamed"`) || !strings.Contains(string(body), `"stream":true`) || !strings.Contains(string(body), `"system":"configured"`) {
			t.Errorf("Anthropic upstream lost converted stream fields: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicEvents)
	}))
	defer anthropicServer.Close()

	var geminiCalls int
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geminiCalls++
		if (r.URL.Path != "/v1beta/models/gemini-streamed:streamGenerateContent" && r.URL.Path != "/v1beta/models/gemini-sync:generateContent") || r.Header.Get("x-goog-api-key") != "gemini-stream-key" {
			t.Errorf("Gemini upstream got incorrect route or credentials: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"maxOutputTokens":16`) && !strings.Contains(string(body), `"maxOutputTokens":12`) {
			t.Errorf("Gemini upstream lost the converted limit: %s", body)
		}
		if r.URL.Path == "/v1beta/models/gemini-sync:generateContent" {
			if r.URL.Query().Get("alt") != "" {
				t.Errorf("Gemini sync conversion requested an event stream: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"rsync","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"responseId\":\"rgen\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n"+
			"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":4,\"totalTokenCount\":6}}\n\n")
	}))
	defer geminiServer.Close()

	var chatCalls int
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer chat-stream-key" {
			t.Errorf("Chat upstream got incorrect route or credentials: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"chat-streamed"`) || !strings.Contains(string(body), `"stream":true`) || !strings.Contains(string(body), `"max_completion_tokens":8`) || !strings.Contains(string(body), `"include_usage":true`) {
			t.Errorf("Chat upstream lost converted stream fields: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-s1\",\"object\":\"chat.completion.chunk\",\"system_fingerprint\":\"fp_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"+
			"data: {\"id\":\"chatcmpl-s1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hey\"}}]}\n\n"+
			"data: {\"id\":\"chatcmpl-s1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":2}}\n\n"+
			"data: [DONE]\n\n")
	}))
	defer chatServer.Close()

	for _, item := range []struct {
		kind  int
		key   string
		url   string
		name  string
		mapTo string
	}{
		{channeltype.Anthropic, "anthropic-stream-key", anthropicServer.URL, "claude-streamed", ""},
		{channeltype.Gemini, "gemini-stream-key", geminiServer.URL, "gemini-streamed", ""},
		{channeltype.OpenAI, "chat-stream-key", chatServer.URL, "chat-streamed", ""},
		{channeltype.Gemini, "gemini-stream-key", geminiServer.URL, "gemini-sync", ""},
	} {
		streamURL := item.url
		channel := model.Channel{Type: item.kind, Key: item.key, Name: item.name, Status: model.ChannelStatusEnabled, Group: "default", Models: item.name, BaseURL: &streamURL}
		if item.mapTo != "" {
			mapping := `{"` + item.name + `":"` + item.mapTo + `"}`
			channel.ModelMapping = &mapping
		}
		if err := channel.Insert(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.Channel{}).Where("name IN ?", []string{"claude-streamed", "gemini-sync"}).Update("system_prompt", "configured").Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetRelayRouter(r)

	// Streamed Chat Completions onto an Anthropic channel.
	chatToAnthropicRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-streamed","messages":[{"role":"system","content":"original"},{"role":"user","content":"hi"}],"stream":true}`))
	chatToAnthropicRequest.Header.Set("Content-Type", "application/json")
	chatToAnthropicRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	chatToAnthropicResponse := httptest.NewRecorder()
	r.ServeHTTP(chatToAnthropicResponse, chatToAnthropicRequest)
	convertedBody := chatToAnthropicResponse.Body.String()
	if chatToAnthropicResponse.Code != http.StatusOK || !strings.Contains(convertedBody, `"id":"msg_stream"`) || !strings.Contains(convertedBody, `"content":"hi"`) || !strings.Contains(convertedBody, `"finish_reason":"stop"`) || !strings.Contains(convertedBody, "[DONE]") {
		t.Fatalf("streamed Chat to Anthropic conversion failed: status %d body %s", chatToAnthropicResponse.Code, convertedBody)
	}
	if strings.Contains(convertedBody, "message_start") || strings.Contains(convertedBody, "content_block") {
		t.Fatalf("Anthropic events leaked into the Chat stream: %s", convertedBody)
	}
	var anthropicStreamLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "claude-streamed", model.LogTypeConsume).Order("id desc").First(&anthropicStreamLog).Error; err != nil || anthropicStreamLog.PromptTokens != 3 || anthropicStreamLog.CompletionTokens != 4 || !anthropicStreamLog.IsStream || !anthropicStreamLog.SystemPromptReset {
		t.Fatalf("streamed Chat to Anthropic usage was not settled: %+v err %v", anthropicStreamLog, err)
	}

	// Streamed Chat Completions onto a Gemini channel.
	chatToGeminiRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-streamed","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":16}`))
	chatToGeminiRequest.Header.Set("Content-Type", "application/json")
	chatToGeminiRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	chatToGeminiResponse := httptest.NewRecorder()
	r.ServeHTTP(chatToGeminiResponse, chatToGeminiRequest)
	geminiStreamBody := chatToGeminiResponse.Body.String()
	if chatToGeminiResponse.Code != http.StatusOK || !strings.Contains(geminiStreamBody, `"id":"rgen"`) || !strings.Contains(geminiStreamBody, `"finish_reason":"stop"`) || !strings.Contains(geminiStreamBody, "[DONE]") {
		t.Fatalf("streamed Chat to Gemini conversion failed: status %d body %s", chatToGeminiResponse.Code, geminiStreamBody)
	}
	var geminiStreamText string
	for _, line := range strings.Split(geminiStreamBody, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.TrimSpace(line) == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) == nil && len(chunk.Choices) == 1 {
			geminiStreamText += chunk.Choices[0].Delta.Content
		}
	}
	if geminiStreamText != "hello" {
		t.Fatalf("streamed Chat to Gemini lost content: %q in %s", geminiStreamText, geminiStreamBody)
	}
	var geminiStreamLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "gemini-streamed", model.LogTypeConsume).Order("id desc").First(&geminiStreamLog).Error; err != nil || geminiStreamLog.PromptTokens != 2 || geminiStreamLog.CompletionTokens != 4 || !geminiStreamLog.IsStream {
		t.Fatalf("streamed Chat to Gemini usage was not settled: %+v err %v", geminiStreamLog, err)
	}

	// Streamed Messages falling back to an OpenAI Chat channel.
	messagesToChatRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"chat-streamed","messages":[{"role":"user","content":"hi"}],"max_tokens":8,"stream":true}`))
	messagesToChatRequest.Header.Set("Content-Type", "application/json")
	messagesToChatRequest.Header.Set("x-api-key", "sk-gateway-key")
	messagesToChatResponse := httptest.NewRecorder()
	r.ServeHTTP(messagesToChatResponse, messagesToChatRequest)
	anthropicClientBody := messagesToChatResponse.Body.String()
	for _, want := range []string{`"id":"chatcmpl-s1"`, `"text":"hey"`, `"stop_reason":"end_turn"`, `"input_tokens":6`, `"output_tokens":2`, "event: message_stop"} {
		if !strings.Contains(anthropicClientBody, want) {
			t.Fatalf("streamed Messages to Chat conversion is missing %q: status %d body %s", want, messagesToChatResponse.Code, anthropicClientBody)
		}
	}
	if strings.Contains(anthropicClientBody, "chat.completion.chunk") || strings.Contains(anthropicClientBody, "[DONE]") {
		t.Fatalf("Chat chunks leaked into the Messages stream: %s", anthropicClientBody)
	}
	var messagesStreamLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "chat-streamed", model.LogTypeConsume).Order("id desc").First(&messagesStreamLog).Error; err != nil || messagesStreamLog.PromptTokens != 6 || messagesStreamLog.CompletionTokens != 2 || !messagesStreamLog.IsStream {
		t.Fatalf("streamed Messages to Chat usage was not settled: %+v err %v", messagesStreamLog, err)
	}

	// Synchronous Messages falling back to a Gemini channel when no Anthropic
	// or OpenAI channel serves the model.
	messagesToGeminiRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gemini-sync","messages":[{"role":"user","content":"hi"}],"max_tokens":12}`))
	messagesToGeminiRequest.Header.Set("Content-Type", "application/json")
	messagesToGeminiRequest.Header.Set("x-api-key", "sk-gateway-key")
	messagesToGeminiResponse := httptest.NewRecorder()
	r.ServeHTTP(messagesToGeminiResponse, messagesToGeminiRequest)
	var messagesResult struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if messagesToGeminiResponse.Code != http.StatusOK || json.Unmarshal(messagesToGeminiResponse.Body.Bytes(), &messagesResult) != nil || messagesResult.Type != "message" || messagesResult.Model != "gemini-sync" || messagesResult.StopReason != "end_turn" || len(messagesResult.Content) != 1 || messagesResult.Content[0].Text != "hello" || messagesResult.Usage.Input != 2 || messagesResult.Usage.Output != 3 {
		t.Fatalf("Messages to Gemini conversion failed: status %d body %s", messagesToGeminiResponse.Code, messagesToGeminiResponse.Body.String())
	}
	var geminiSyncLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "gemini-sync", model.LogTypeConsume).Order("id desc").First(&geminiSyncLog).Error; err != nil || geminiSyncLog.PromptTokens != 2 || geminiSyncLog.CompletionTokens != 3 || !geminiSyncLog.SystemPromptReset {
		t.Fatalf("Messages to Gemini usage was not settled: %+v err %v", geminiSyncLog, err)
	}
	if anthropicCalls != 1 || geminiCalls != 2 || chatCalls != 1 {
		t.Fatalf("upstream call counts changed: anthropic %d gemini %d chat %d", anthropicCalls, geminiCalls, chatCalls)
	}
}
