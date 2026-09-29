package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The official clients use a local gateway and local provider fixtures. No
// external API credentials or live provider access are involved.
func TestPhase3OfficialGoClients(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := client.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:phase3-sdk?mode=memory&cache=shared"), &gorm.Config{})
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
	common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite = false, false, true
	config.RetryTimes = 0
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		client.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes = previousRedis, previousMemoryCache, previousRetries
		common.UsingSQLite = previousSQLite
		_ = sqlDB.Close()
	})
	user := model.User{Username: "phase3-sdk", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "phase3-gateway-key", Name: "phase3-sdk", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	openAIProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/chat/completions") || r.Header.Get("Authorization") != "Bearer provider-openai" {
			t.Errorf("OpenAI provider request: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"upstream-openai"`) {
			t.Errorf("OpenAI model mapping: %s", body)
		}
		if strings.Contains(string(body), "aGVsbG8=") && !strings.Contains(string(body), "data:image/png;base64,aGVsbG8=") {
			t.Errorf("Messages image was not converted into a Chat data URL: %s", body)
		}
		if strings.Contains(string(body), "fail") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"openai busy","code":"busy"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chat_sdk\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-openai\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"openai ok\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"chat_sdk\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-openai\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"+
				"data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat_sdk","object":"chat.completion","created":1,"model":"upstream-openai","choices":[{"index":0,"message":{"role":"assistant","content":"openai ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer openAIProvider.Close()
	anthropicProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "provider-anthropic" {
			t.Errorf("Anthropic provider request: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"upstream-anthropic"`) {
			t.Errorf("Anthropic model mapping: %s", body)
		}
		if strings.Contains(string(body), "aGVsbG8=") && !strings.Contains(string(body), `"media_type":"image/png"`) {
			t.Errorf("Chat image was not converted into a Messages image source: %s", body)
		}
		if strings.Contains(string(body), "fail") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"anthropic busy"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_sdk\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-anthropic\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic ok\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":2}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_sdk","type":"message","role":"assistant","model":"upstream-anthropic","content":[{"type":"text","text":"anthropic ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer anthropicProvider.Close()
	ollamaProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "Bearer provider-ollama" {
			t.Errorf("Ollama provider request: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"upstream-ollama"`) {
			t.Errorf("Ollama model mapping: %s", body)
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			if strings.Contains(string(body), "truncate") {
				_, _ = io.WriteString(w, "{\"model\":\"upstream-ollama\",\"message\":{\"role\":\"assistant\",\"content\":\"partial\"},\"done\":false}\n")
				return
			}
			_, _ = io.WriteString(w, "{\"model\":\"upstream-ollama\",\"message\":{\"role\":\"assistant\",\"content\":\"ollama ok\"},\"done\":false}\n"+
				"{\"model\":\"upstream-ollama\",\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"done\":true,\"prompt_eval_count\":3,\"eval_count\":2}\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"upstream-ollama","message":{"role":"assistant","content":"ollama ok"},"done":true,"prompt_eval_count":3,"eval_count":2}`)
	}))
	defer ollamaProvider.Close()
	failingOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"retry Ollama"}}`)
	}))
	defer failingOllama.Close()
	openAIURL, anthropicURL, ollamaURL := openAIProvider.URL, anthropicProvider.URL, ollamaProvider.URL
	failingOllamaURL := failingOllama.URL
	openAIMapping, anthropicMapping := `{"chat-sdk-model":"upstream-openai"}`, `{"messages-sdk-model":"upstream-anthropic"}`
	compatibleMapping := `{"compatible-sdk-model":"upstream-openai"}`
	ollamaMapping := `{"ollama-sdk-model":"upstream-ollama"}`
	retryOllamaMapping := `{"ollama-retry-model":"upstream-ollama"}`
	highPriority, lowPriority := int64(10), int64(0)
	var successfulOllamaRetry *model.Channel
	for _, channel := range []*model.Channel{
		{Type: channeltype.OpenAI, Key: "provider-openai", Name: "sdk-openai", Status: model.ChannelStatusEnabled, Group: "default", Models: "chat-sdk-model", ModelMapping: &openAIMapping, BaseURL: &openAIURL},
		{Type: channeltype.OpenAICompatible, Key: "provider-openai", Name: "sdk-compatible", Status: model.ChannelStatusEnabled, Group: "default", Models: "compatible-sdk-model", ModelMapping: &compatibleMapping, BaseURL: &openAIURL},
		{Type: channeltype.Anthropic, Key: "provider-anthropic", Name: "sdk-anthropic", Status: model.ChannelStatusEnabled, Group: "default", Models: "messages-sdk-model", ModelMapping: &anthropicMapping, BaseURL: &anthropicURL},
		{Type: channeltype.Ollama, Key: "provider-ollama", Name: "sdk-ollama", Status: model.ChannelStatusEnabled, Group: "default", Models: "ollama-sdk-model", ModelMapping: &ollamaMapping, BaseURL: &ollamaURL},
		{Type: channeltype.Ollama, Key: "provider-ollama", Name: "sdk-ollama-fail", Status: model.ChannelStatusEnabled, Group: "default", Models: "ollama-retry-model", ModelMapping: &retryOllamaMapping, BaseURL: &failingOllamaURL, Priority: &highPriority},
		{Type: channeltype.Ollama, Key: "provider-ollama", Name: "sdk-ollama-retry", Status: model.ChannelStatusEnabled, Group: "default", Models: "ollama-retry-model", ModelMapping: &retryOllamaMapping, BaseURL: &ollamaURL, Priority: &lowPriority},
	} {
		if err := channel.Insert(); err != nil {
			t.Fatal(err)
		}
		if channel.Name == "sdk-ollama-retry" {
			successfulOllamaRetry = channel
		}
	}
	gin.SetMode(gin.TestMode)
	routes := gin.New()
	SetRelayRouter(routes)
	gateway := httptest.NewServer(routes)
	defer gateway.Close()
	openAIClient := openai.NewClient(openaioption.WithBaseURL(gateway.URL+"/v1"), openaioption.WithAPIKey("sk-phase3-gateway-key"), openaioption.WithMaxRetries(0))
	anthropicClient := anthropic.NewClient(anthropicoption.WithBaseURL(gateway.URL), anthropicoption.WithAPIKey("sk-phase3-gateway-key"), anthropicoption.WithMaxRetries(0))
	ctx := context.Background()
	chat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "chat-sdk-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}})
	if err != nil || len(chat.Choices) != 1 || chat.Choices[0].Message.Content != "openai ok" {
		t.Fatalf("official OpenAI client native Chat: %+v, %v", chat, err)
	}
	compatibleChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "compatible-sdk-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}})
	if err != nil || len(compatibleChat.Choices) != 1 || compatibleChat.Choices[0].Message.Content != "openai ok" {
		t.Fatalf("official OpenAI client compatible Chat: %+v, %v", compatibleChat, err)
	}
	ollamaChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "ollama-sdk-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}})
	if err != nil || len(ollamaChat.Choices) != 1 || ollamaChat.Choices[0].Message.Content != "ollama ok" {
		t.Fatalf("official OpenAI client adapter-backed Chat: %+v, %v", ollamaChat, err)
	}
	config.RetryTimes = 1
	retriedChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "ollama-retry-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}})
	config.RetryTimes = 0
	if err != nil || len(retriedChat.Choices) != 1 || retriedChat.Choices[0].Message.Content != "ollama ok" {
		t.Fatalf("official OpenAI client adapter-backed retry: %+v, %v", retriedChat, err)
	}
	var retryLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "upstream-ollama", model.LogTypeConsume).Order("id desc").First(&retryLog).Error; err != nil || retryLog.ChannelId != successfulOllamaRetry.Id {
		t.Fatalf("adapter-backed retry settled on wrong channel: %+v, %v", retryLog, err)
	}
	convertedChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "messages-sdk-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}})
	if err != nil || len(convertedChat.Choices) != 1 || convertedChat.Choices[0].Message.Content != "anthropic ok" {
		t.Fatalf("official OpenAI client converted Chat: %+v, %v", convertedChat, err)
	}
	imageChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "messages-sdk-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart("describe"), openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: "data:image/png;base64,aGVsbG8="}),
	})}})
	if err != nil || len(imageChat.Choices) != 1 || imageChat.Choices[0].Message.Content != "anthropic ok" {
		t.Fatalf("official OpenAI client converted image input: %+v, %v", imageChat, err)
	}
	params := func(modelName string) anthropic.MessageNewParams {
		return anthropic.MessageNewParams{Model: anthropic.Model(modelName), MaxTokens: 16, Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hello"))}}
	}
	message, err := anthropicClient.Messages.New(ctx, params("messages-sdk-model"))
	if err != nil || message.ID != "msg_sdk" || len(message.Content) != 1 || message.Content[0].Text != "anthropic ok" {
		t.Fatalf("official Anthropic client native Messages: %+v, %v", message, err)
	}
	convertedMessage, err := anthropicClient.Messages.New(ctx, params("chat-sdk-model"))
	if err != nil || convertedMessage.ID != "chat_sdk" || len(convertedMessage.Content) != 1 || convertedMessage.Content[0].Text != "openai ok" {
		t.Fatalf("official Anthropic client converted Messages: %+v, %v", convertedMessage, err)
	}
	imageMessage, err := anthropicClient.Messages.New(ctx, anthropic.MessageNewParams{Model: "chat-sdk-model", MaxTokens: 16, Messages: []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("describe"), anthropic.NewImageBlockBase64("image/png", "aGVsbG8=")),
	}})
	if err != nil || imageMessage.ID != "chat_sdk" {
		t.Fatalf("official Anthropic client converted image input: %+v, %v", imageMessage, err)
	}
	if _, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: "messages-sdk-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("fail")}}); err == nil || !strings.Contains(err.Error(), "anthropic busy") {
		t.Fatalf("official OpenAI client did not decode converted provider error: %v", err)
	}
	if _, err := anthropicClient.Messages.New(ctx, anthropic.MessageNewParams{Model: "chat-sdk-model", MaxTokens: 16, Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("fail"))}}); err == nil || !strings.Contains(err.Error(), "openai busy") {
		t.Fatalf("official Anthropic client did not decode converted provider error: %v", err)
	}
	for _, fixture := range []struct {
		path string
		body string
		want string
	}{
		{"/v1/chat/completions", `{"model":"messages-sdk-model","messages":[{"role":"user","content":"fail"}]}`, `"upstream_type":"rate_limit_error"`},
		{"/v1/messages", `{"model":"chat-sdk-model","max_tokens":16,"messages":[{"role":"user","content":"fail"}]}`, `"code":"busy"`},
	} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+fixture.path, strings.NewReader(fixture.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer sk-phase3-gateway-key")
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), fixture.want) {
			t.Fatalf("converted %s error lost provider detail: status %d body %s", fixture.path, response.StatusCode, body)
		}
	}
	for _, modelName := range []string{"chat-sdk-model", "messages-sdk-model", "ollama-sdk-model"} {
		stream := openAIClient.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{Model: modelName, Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}})
		var content string
		for stream.Next() {
			for _, choice := range stream.Current().Choices {
				content += choice.Delta.Content
			}
		}
		if err := stream.Err(); err != nil {
			t.Fatalf("official OpenAI stream %s: %v", modelName, err)
		}
		_ = stream.Close()
		if want := map[string]string{"chat-sdk-model": "openai ok", "messages-sdk-model": "anthropic ok", "ollama-sdk-model": "ollama ok"}[modelName]; content != want {
			t.Fatalf("official OpenAI stream %s yielded %q, want %q", modelName, content, want)
		}
	}
	truncatedRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"ollama-sdk-model","messages":[{"role":"user","content":"truncate"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	truncatedRequest.Header.Set("Authorization", "Bearer sk-phase3-gateway-key")
	truncatedRequest.Header.Set("Content-Type", "application/json")
	truncatedResponse, err := http.DefaultClient.Do(truncatedRequest)
	if err != nil {
		t.Fatal(err)
	}
	truncatedBody, err := io.ReadAll(truncatedResponse.Body)
	_ = truncatedResponse.Body.Close()
	if err != nil || !strings.Contains(string(truncatedBody), "partial") || strings.Contains(string(truncatedBody), "[DONE]") {
		t.Fatalf("truncated provider stream was marked complete: %s, %v", truncatedBody, err)
	}
	for _, modelName := range []string{"messages-sdk-model", "chat-sdk-model"} {
		stream := anthropicClient.Messages.NewStreaming(ctx, params(modelName))
		var content string
		var stopped bool
		for stream.Next() {
			event := stream.Current()
			if event.Type == "content_block_delta" {
				content += event.Delta.Text
			}
			if event.Type == "message_stop" {
				stopped = true
			}
		}
		if err := stream.Err(); err != nil {
			t.Fatalf("official Anthropic stream %s: %v", modelName, err)
		}
		_ = stream.Close()
		if want := map[string]string{"messages-sdk-model": "anthropic ok", "chat-sdk-model": "openai ok"}[modelName]; content != want || !stopped {
			t.Fatalf("official Anthropic stream %s yielded %q, stopped %v, want %q", modelName, content, stopped, want)
		}
	}
}
