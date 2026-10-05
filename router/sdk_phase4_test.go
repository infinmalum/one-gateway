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
	"google.golang.org/genai"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The official Gemini, OpenAI, and Anthropic clients run against a local
// gateway and local provider fixtures. No external API credentials or live
// provider access are involved.
func TestPhase4OfficialGeminiClient(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := client.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:phase4-sdk?mode=memory&cache=shared"), &gorm.Config{})
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
	user := model.User{Username: "phase4-sdk", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "phase4-gateway-key", Name: "phase4-sdk", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	geminiProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "gemini-provider-key" {
			t.Errorf("Gemini provider credentials: %v", r.Header)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/") || (!strings.HasSuffix(r.URL.Path, ":generateContent") && !strings.HasSuffix(r.URL.Path, ":streamGenerateContent")) {
			t.Errorf("Gemini provider route: %s", r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		requestBody := string(body)
		switch {
		case strings.Contains(requestBody, `"functionResponse"`):
			if !strings.Contains(requestBody, `"functionResponse":{"id":"gem-call-1","name":"lookup","response":{"result":"found"}}`) || !strings.Contains(requestBody, `"functionCall":{"args":{"q":"hi"},"id":"gem-call-1","name":"lookup"}`) {
				t.Errorf("tool result round trip lost fields: %s", requestBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"gem_done","modelVersion":"gemini-tool-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"tool done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"totalTokenCount":11}}`)
		case strings.Contains(requestBody, `"functionDeclarations"`):
			if r.URL.Path != "/v1beta/models/gemini-tool-upstream:generateContent" || !strings.Contains(requestBody, `"toolConfig":{"functionCallingConfig":{"allowedFunctionNames":["lookup"],"mode":"ANY"}}`) {
				t.Errorf("Chat tool request was not converted: %s %s", r.URL, requestBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"gem_tool","modelVersion":"gemini-tool-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"Calling"},{"functionCall":{"id":"gem-call-1","name":"lookup","args":{"q":"hi"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":4,"totalTokenCount":10}}`)
		case strings.Contains(requestBody, `"inlineData"`):
			if !strings.Contains(requestBody, `"inlineData":{"data":"aGVsbG8=","mimeType":"image/png"}`) {
				t.Errorf("Chat image was not converted into inline data: %s", requestBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"gem_media","candidates":[{"content":{"role":"model","parts":[{"text":"saw media"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`)
		case strings.Contains(r.URL.Path, "gemini-think-upstream") && strings.Contains(requestBody, `"thoughtSignature"`):
			if !strings.Contains(requestBody, `{"text":"pondering","thought":true,"thoughtSignature":"c2ln"}`) {
				t.Errorf("thinking round trip was not converted: %s %s", r.URL, requestBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"gem_round2","modelVersion":"gemini-think-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"round two"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`)
		case strings.Contains(r.URL.Path, "gemini-think-upstream"):
			if r.URL.Path != "/v1beta/models/gemini-think-upstream:generateContent" {
				t.Errorf("thinking request route: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"gem_think","modelVersion":"gemini-think-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"pondering","thought":true},{"thoughtSignature":"c2ln"},{"text":"visible answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":6,"thoughtsTokenCount":2,"totalTokenCount":12}}`)
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			if r.URL.Query().Get("alt") != "sse" || r.URL.Path != "/v1beta/models/gemini-stream-upstream:streamGenerateContent" {
				t.Errorf("native Gemini stream route: %s", r.URL)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"responseId\":\"gem_stream\",\"modelVersion\":\"gemini-stream-upstream\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"gemini \"}]}}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"totalTokenCount\":5}}\n\n"+
				"data: {\"responseId\":\"gem_stream\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"stream\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4,\"totalTokenCount\":7}}\n\n")
		default:
			if r.URL.Path == "/v1beta/models/future-gemini-model:generateContent" {
				if !strings.Contains(requestBody, `"contents"`) {
					t.Errorf("new model request lost contents: %s", requestBody)
				}
			} else if r.URL.Path != "/v1beta/models/gemini-upstream:generateContent" {
				t.Errorf("unexpected native Gemini route: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"responseId":"gem_sdk","modelVersion":"gemini-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"gemini ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`)
		}
	}))
	defer geminiProvider.Close()
	providerURL := geminiProvider.URL
	sdkMapping := `{"gemini-sdk-model":"gemini-upstream"}`
	toolMapping := `{"gemini-tool-model":"gemini-tool-upstream"}`
	thinkMapping := `{"gemini-think-model":"gemini-think-upstream"}`
	streamMapping := `{"gemini-stream-model":"gemini-stream-upstream"}`
	newMapping := `{"gemini-new-model":"future-gemini-model"}`
	for _, channel := range []*model.Channel{
		{Type: channeltype.Gemini, Key: "gemini-provider-key", Name: "sdk-gemini", Status: model.ChannelStatusEnabled, Group: "default", Models: "gemini-sdk-model", BaseURL: &providerURL, ModelMapping: &sdkMapping},
		{Type: channeltype.Gemini, Key: "gemini-provider-key", Name: "sdk-gemini-new", Status: model.ChannelStatusEnabled, Group: "default", Models: "gemini-new-model", BaseURL: &providerURL, ModelMapping: &newMapping},
		{Type: channeltype.Gemini, Key: "gemini-provider-key", Name: "sdk-gemini-tool", Status: model.ChannelStatusEnabled, Group: "default", Models: "gemini-tool-model", BaseURL: &providerURL, ModelMapping: &toolMapping},
		{Type: channeltype.Gemini, Key: "gemini-provider-key", Name: "sdk-gemini-think", Status: model.ChannelStatusEnabled, Group: "default", Models: "gemini-think-model", BaseURL: &providerURL, ModelMapping: &thinkMapping},
		{Type: channeltype.Gemini, Key: "gemini-provider-key", Name: "sdk-gemini-stream", Status: model.ChannelStatusEnabled, Group: "default", Models: "gemini-stream-model", BaseURL: &providerURL, ModelMapping: &streamMapping},
	} {
		if err := channel.Insert(); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	routes := gin.New()
	SetRelayRouter(routes)
	gateway := httptest.NewServer(routes)
	defer gateway.Close()
	// The official Gemini client reads its gateway token from the environment.
	t.Setenv("GOOGLE_API_KEY", "sk-phase4-gateway-key")
	geminiClient, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  &http.Client{},
		HTTPOptions: genai.HTTPOptions{BaseURL: gateway.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	native, err := geminiClient.Models.GenerateContent(ctx, "gemini-sdk-model", genai.Text("hello"), nil)
	if err != nil || native.Text() != "gemini ok" || native.UsageMetadata.TotalTokenCount != 5 {
		t.Fatalf("official Gemini client native GenerateContent: %+v, %v", native, err)
	}
	newModel, err := geminiClient.Models.GenerateContent(ctx, "gemini-new-model", genai.Text("hello"), nil)
	if err != nil || newModel.Text() != "gemini ok" {
		t.Fatalf("official Gemini client new model ID: %+v, %v", newModel, err)
	}
	var streamed string
	for chunk, err := range geminiClient.Models.GenerateContentStream(ctx, "gemini-stream-model", genai.Text("hello"), nil) {
		if err != nil {
			t.Fatalf("official Gemini client stream: %v", err)
		}
		streamed += chunk.Text()
	}
	if streamed != "gemini stream" {
		t.Fatalf("official Gemini client stream yielded %q", streamed)
	}
	openAIClient := openai.NewClient(openaioption.WithBaseURL(gateway.URL+"/v1"), openaioption.WithAPIKey("sk-phase4-gateway-key"), openaioption.WithMaxRetries(0))
	toolTools := []openai.ChatCompletionToolUnionParam{openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
		Name:        "lookup",
		Description: openai.String("Find an entry"),
		Parameters:  openai.FunctionParameters{"type": "object", "properties": map[string]any{"q": map[string]string{"type": "string"}}},
	})}
	toolChoice := openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{Name: "lookup"})
	toolChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:      "gemini-tool-model",
		Messages:   []openai.ChatCompletionMessageParamUnion{openai.UserMessage("look up hi")},
		Tools:      toolTools,
		ToolChoice: toolChoice,
	})
	if err != nil || len(toolChat.Choices) != 1 {
		t.Fatalf("official OpenAI client converted tool request: %+v, %v", toolChat, err)
	}
	calls := toolChat.Choices[0].Message.ToolCalls
	if toolChat.Choices[0].Message.Content != "Calling" || len(calls) != 1 || calls[0].Function.Name != "lookup" || calls[0].ID != "gem-call-1" {
		t.Fatalf("official OpenAI client lost the converted tool call: %+v", toolChat.Choices[0].Message)
	}
	if toolChat.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("official OpenAI client finish reason: %q", toolChat.Choices[0].FinishReason)
	}
	followUp := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("look up hi"),
		toolChat.Choices[0].Message.ToParam(),
		openai.ToolMessage("found", "gem-call-1"),
	}
	completed, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: "gemini-tool-model", Messages: followUp,
		Tools:      toolTools,
		ToolChoice: toolChoice,
	})
	if err != nil || len(completed.Choices) != 1 || completed.Choices[0].Message.Content != "tool done" {
		t.Fatalf("official OpenAI client tool result round trip: %+v, %v", completed, err)
	}
	imageChat, err := openAIClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    "gemini-sdk-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: "data:image/png;base64,aGVsbG8="})})},
	})
	if err != nil || imageChat.Choices[0].Message.Content != "saw media" {
		t.Fatalf("official OpenAI client converted image input: %+v, %v", imageChat, err)
	}
	anthropicClient := anthropic.NewClient(anthropicoption.WithBaseURL(gateway.URL), anthropicoption.WithAPIKey("sk-phase4-gateway-key"), anthropicoption.WithMaxRetries(0))
	thinking, err := anthropicClient.Messages.New(ctx, anthropic.MessageNewParams{
		Model: "gemini-think-model", MaxTokens: 64,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("reason"))},
	})
	if err != nil || len(thinking.Content) != 2 {
		t.Fatalf("official Anthropic client converted thinking response: %+v, %v", thinking, err)
	}
	if thinking.Content[0].Type != "thinking" || thinking.Content[0].Thinking != "pondering" || thinking.Content[0].Signature != "c2ln" {
		t.Fatalf("official Anthropic client lost the thinking block: %+v", thinking.Content[0])
	}
	if thinking.Content[1].Type != "text" || thinking.Content[1].Text != "visible answer" || thinking.StopReason != anthropic.StopReasonEndTurn {
		t.Fatalf("official Anthropic client lost the answer: %+v", thinking.Content[1])
	}
	roundTrip, err := anthropicClient.Messages.New(ctx, anthropic.MessageNewParams{
		Model: "gemini-think-model", MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("reason")),
			anthropic.NewAssistantMessage(anthropic.NewThinkingBlock("c2ln", "pondering"), anthropic.NewTextBlock("visible answer")),
			anthropic.NewUserMessage(anthropic.NewTextBlock("continue")),
		},
	})
	if err != nil || len(roundTrip.Content) != 1 || roundTrip.Content[0].Text != "round two" {
		t.Fatalf("official Anthropic client thinking round trip: %+v, %v", roundTrip, err)
	}
}
