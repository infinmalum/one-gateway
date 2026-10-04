package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNativeRoutesSelectMatchingProtocolAndPreserveWireData(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := client.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:native-routes?mode=memory&cache=shared"), &gorm.Config{})
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
	common.RedisEnabled, config.MemoryCacheEnabled = false, false
	common.UsingSQLite = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		client.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled = previousRedis, previousMemoryCache
		common.UsingSQLite = previousSQLite
		config.RetryTimes = previousRetries
		_ = sqlDB.Close()
	})
	user := model.User{Username: "native-test", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "gateway-key", Name: "native", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	anthropicBody := `{"type":"message","model":"claude-upstream","content":[],"usage":{"input_tokens":3,"output_tokens":4},"future":true}`
	anthropicEvents := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"reasoning\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-provider-key" || r.Header.Get("anthropic-beta") != "test-beta" {
			t.Errorf("Anthropic upstream got incorrect route or headers: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"cancel":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if strings.Contains(string(body), `"fail":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		if !strings.Contains(string(body), `"model":"claude-upstream"`) || !strings.Contains(string(body), `"future":{"preserve":true}`) {
			t.Errorf("Anthropic upstream lost model mapping or extension: %s", body)
		}
		if strings.Contains(string(body), `"stream":true`) {
			if !strings.Contains(string(body), `"system":"configured"`) || !strings.Contains(string(body), `"type":"image"`) || !strings.Contains(string(body), `"tools"`) {
				t.Errorf("Anthropic stream lost native fields: %s", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, anthropicEvents)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicBody)
	}))
	defer anthropicServer.Close()
	geminiEvents := "data: {\"candidates\":[],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":5},\"newField\":true}\n\n"
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "gemini-provider-key" {
			t.Errorf("Gemini upstream got incorrect route or headers: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, ":generateContent") {
			if (r.URL.Path != "/v1/models/gemini-upstream:generateContent" && r.URL.Path != "/v1beta/models/gemini-upstream:generateContent" && r.URL.Path != "/v1beta/models/future-model-id:generateContent") || r.URL.Query().Get("alt") != "" || !strings.Contains(string(body), `"inlineData"`) {
				t.Errorf("Gemini normal request changed: %s %s", r.URL, body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3},"future":true}`)
			return
		}
		if (r.URL.Path != "/v1beta/models/gemini-upstream:streamGenerateContent" && r.URL.Path != "/v1/models/gemini-upstream:streamGenerateContent") || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("Gemini streaming route changed: %s", r.URL)
		}
		if string(body) != `{"contents":[{"parts":[{"text":"hello"}]}],"future":42}` {
			t.Errorf("Gemini request body changed: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, geminiEvents)
	}))
	defer geminiServer.Close()
	for _, item := range []struct {
		kind  int
		key   string
		url   string
		mapTo string
	}{
		{channeltype.Anthropic, "anthropic-provider-key", anthropicServer.URL, "claude-upstream"},
		{channeltype.Gemini, "gemini-provider-key", geminiServer.URL, "gemini-upstream"},
	} {
		baseURL, mapping := item.url, `{"alias":"`+item.mapTo+`"}`
		channel := model.Channel{Type: item.kind, Key: item.key, Name: item.mapTo, Status: model.ChannelStatusEnabled, Group: "default", Models: "alias", BaseURL: &baseURL, ModelMapping: &mapping}
		if err := channel.Insert(); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetRelayRouter(r)
	legacyModelRequest := httptest.NewRequest(http.MethodGet, "/v1/models/alias", nil)
	legacyModelRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	legacyModelResponse := httptest.NewRecorder()
	r.ServeHTTP(legacyModelResponse, legacyModelRequest)
	if legacyModelResponse.Code != http.StatusOK {
		t.Fatalf("native route registration broke GET /v1/models: %d %s", legacyModelResponse.Code, legacyModelResponse.Body.String())
	}

	anthropicRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":"hello"}],"max_tokens":8,"future":{"preserve":true}}`))
	anthropicRequest.Header.Set("Content-Type", "application/json")
	anthropicRequest.Header.Set("x-api-key", "sk-gateway-key")
	anthropicRequest.Header.Set("anthropic-beta", "test-beta")
	anthropicResponse := httptest.NewRecorder()
	r.ServeHTTP(anthropicResponse, anthropicRequest)
	if anthropicResponse.Code != http.StatusOK || anthropicResponse.Body.String() != anthropicBody {
		t.Fatalf("native Anthropic response: status %d body %s", anthropicResponse.Code, anthropicResponse.Body.String())
	}

	geminiRequest := httptest.NewRequest(http.MethodPost, "/v1beta/models/alias:streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"future":42}`))
	geminiRequest.Header.Set("Content-Type", "application/json")
	geminiRequest.Header.Set("x-goog-api-key", "sk-gateway-key")
	geminiResponse := httptest.NewRecorder()
	r.ServeHTTP(geminiResponse, geminiRequest)
	if geminiResponse.Code != http.StatusOK || geminiResponse.Body.String() != geminiEvents {
		t.Fatalf("native Gemini response: status %d body %s", geminiResponse.Code, geminiResponse.Body.String())
	}
	quotaAfterSuccess, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	failureRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[],"max_tokens":8,"fail":true}`))
	failureRequest.Header.Set("Content-Type", "application/json")
	failureRequest.Header.Set("x-api-key", "sk-gateway-key")
	failureRequest.Header.Set("anthropic-beta", "test-beta")
	failureResponse := httptest.NewRecorder()
	r.ServeHTTP(failureResponse, failureRequest)
	if failureResponse.Code != http.StatusTooManyRequests || !strings.Contains(failureResponse.Body.String(), `"rate_limit_error"`) {
		t.Fatalf("upstream error changed: status %d body %s", failureResponse.Code, failureResponse.Body.String())
	}
	quotaAfterFailure, err := model.GetUserQuota(user.Id)
	if err != nil || quotaAfterFailure != quotaAfterSuccess {
		t.Fatalf("failed request was billed: before %d after %d err %v", quotaAfterSuccess, quotaAfterFailure, err)
	}

	var logs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error; err != nil || len(logs) != 2 {
		t.Fatalf("expected two usage logs, got %d: %v", len(logs), err)
	}
	var updated model.User
	if err := db.First(&updated, user.Id).Error; err != nil || updated.Quota >= user.Quota {
		t.Fatalf("native requests were not billed: %+v %v", updated, err)
	}

	if err := db.Model(&model.Channel{}).Where("type = ?", channeltype.Anthropic).Update("system_prompt", "configured").Error; err != nil {
		t.Fatal(err)
	}
	anthropicStreamRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}],"max_tokens":8,"stream":true,"tools":[{"name":"search","input_schema":{"type":"object"}}],"future":{"preserve":true}}`))
	anthropicStreamRequest.Header.Set("x-api-key", "sk-gateway-key")
	anthropicStreamRequest.Header.Set("anthropic-beta", "test-beta")
	anthropicStreamResponse := httptest.NewRecorder()
	r.ServeHTTP(anthropicStreamResponse, anthropicStreamRequest)
	if anthropicStreamResponse.Code != http.StatusOK || anthropicStreamResponse.Body.String() != anthropicEvents {
		t.Fatalf("native Anthropic stream: status %d body %s", anthropicStreamResponse.Code, anthropicStreamResponse.Body.String())
	}
	var promptLog model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id desc").First(&promptLog).Error; err != nil || !promptLog.SystemPromptReset {
		t.Fatalf("native system prompt rewrite was not logged: %+v err %v", promptLog, err)
	}

	geminiNormalRequest := httptest.NewRequest(http.MethodPost, "/v1/models/alias:generateContent", strings.NewReader(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}],"future":42}`))
	geminiNormalRequest.Header.Set("Content-Type", "application/json")
	geminiNormalRequest.Header.Set("x-goog-api-key", "sk-gateway-key")
	geminiNormalResponse := httptest.NewRecorder()
	r.ServeHTTP(geminiNormalResponse, geminiNormalRequest)
	if geminiNormalResponse.Code != http.StatusOK || !strings.Contains(geminiNormalResponse.Body.String(), `"future":true`) {
		t.Fatalf("native Gemini normal response: status %d body %s", geminiNormalResponse.Code, geminiNormalResponse.Body.String())
	}
	for _, fixture := range []struct {
		path string
		body string
		want string
	}{
		{"/v1beta/models/alias:generateContent", `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`, `"future":true`},
		{"/v1/models/alias:streamGenerateContent", `{"contents":[{"parts":[{"text":"hello"}]}],"future":42}`, geminiEvents},
	} {
		request := httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body))
		request.Header.Set("x-goog-api-key", "sk-gateway-key")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), fixture.want) {
			t.Fatalf("Gemini version/action fixture %s: status %d body %s", fixture.path, response.Code, response.Body.String())
		}
	}
	futureBaseURL := geminiServer.URL
	futureGemini := model.Channel{Type: channeltype.Gemini, Key: "gemini-provider-key", Name: "future-model", Status: model.ChannelStatusEnabled, Group: "default", Models: "future-model-id", BaseURL: &futureBaseURL}
	if err := futureGemini.Insert(); err != nil {
		t.Fatal(err)
	}
	futureRequest := httptest.NewRequest(http.MethodPost, "/v1beta/models/future-model-id:generateContent", strings.NewReader(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`))
	futureRequest.Header.Set("x-goog-api-key", "sk-gateway-key")
	futureResponse := httptest.NewRecorder()
	r.ServeHTTP(futureResponse, futureRequest)
	if futureResponse.Code != http.StatusOK || !strings.Contains(futureResponse.Body.String(), `"future":true`) {
		t.Fatalf("new Gemini model ID: status %d body %s", futureResponse.Code, futureResponse.Body.String())
	}

	openAIBaseURL := anthropicServer.URL
	openAIOnly := model.Channel{Type: channeltype.OpenAI, Key: "other", Name: "openai-only", Status: model.ChannelStatusEnabled, Group: "default", Models: "openai-only", BaseURL: &openAIBaseURL}
	if err := openAIOnly.Insert(); err != nil {
		t.Fatal(err)
	}
	unsupportedRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"orphan-model","messages":[],"max_tokens":8,"stream":true}`))
	unsupportedRequest.Header.Set("Content-Type", "application/json")
	unsupportedRequest.Header.Set("x-api-key", "sk-gateway-key")
	unsupportedResponse := httptest.NewRecorder()
	r.ServeHTTP(unsupportedResponse, unsupportedRequest)
	if unsupportedResponse.Code != http.StatusServiceUnavailable || !strings.Contains(unsupportedResponse.Body.String(), `"type":"error"`) || !strings.Contains(unsupportedResponse.Body.String(), "Anthropic") {
		t.Fatalf("unsupported native channel: status %d body %s", unsupportedResponse.Code, unsupportedResponse.Body.String())
	}
	unsupportedGeminiRequest := httptest.NewRequest(http.MethodPost, "/v1beta/models/openai-only:generateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`))
	unsupportedGeminiRequest.Header.Set("x-goog-api-key", "sk-gateway-key")
	unsupportedGeminiResponse := httptest.NewRecorder()
	r.ServeHTTP(unsupportedGeminiResponse, unsupportedGeminiRequest)
	if unsupportedGeminiResponse.Code != http.StatusServiceUnavailable || !strings.Contains(unsupportedGeminiResponse.Body.String(), `"status":"UNAVAILABLE"`) || !strings.Contains(unsupportedGeminiResponse.Body.String(), "Gemini") {
		t.Fatalf("unsupported Gemini channel: status %d body %s", unsupportedGeminiResponse.Code, unsupportedGeminiResponse.Body.String())
	}
	blockedBaseURL := anthropicServer.URL
	blockedChannel := model.Channel{Type: channeltype.Baidu, Key: "blocked-key", Name: "blocked", Status: model.ChannelStatusEnabled, Group: "default", Models: "blocked-model", BaseURL: &blockedBaseURL}
	if err := blockedChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	specificMismatch := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"blocked-model","messages":[],"max_tokens":8,"stream":true}`))
	specificMismatch.Header.Set("Authorization", "Bearer sk-gateway-key-"+strconv.Itoa(blockedChannel.Id))
	specificResponse := httptest.NewRecorder()
	r.ServeHTTP(specificResponse, specificMismatch)
	if specificResponse.Code != http.StatusBadRequest || !strings.Contains(specificResponse.Body.String(), "does not support this protocol") {
		t.Fatalf("specific channel bypassed protocol restriction: status %d body %s", specificResponse.Code, specificResponse.Body.String())
	}

	quotaBeforeCancel, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	cancelContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[],"max_tokens":8,"stream":true,"cancel":true}`)).WithContext(cancelContext)
	cancelRequest.Header.Set("x-api-key", "sk-gateway-key")
	cancelRequest.Header.Set("anthropic-beta", "test-beta")
	cancelResponse := &firstWriteRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{}, 1)}
	finished := make(chan struct{})
	go func() {
		r.ServeHTTP(cancelResponse, cancelRequest)
		close(finished)
	}()
	select {
	case <-cancelResponse.wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream event did not reach the client")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled native stream did not finish")
	}
	quotaAfterCancel, err := model.GetUserQuota(user.Id)
	if err != nil || quotaAfterCancel >= quotaBeforeCancel {
		t.Fatalf("interrupted stream was not settled: before %d after %d err %v", quotaBeforeCancel, quotaAfterCancel, err)
	}
	var cancelLog model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id desc").First(&cancelLog).Error; err != nil || cancelLog.ChannelId != 1 || !strings.Contains(cancelLog.Content, "interrupted") || int64(cancelLog.Quota) != quotaBeforeCancel-quotaAfterCancel {
		t.Fatalf("interrupted stream log or quota is wrong: %+v err %v", cancelLog, err)
	}

	failureAttempts := 0
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failureAttempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"retry me"}}`)
	}))
	defer failingServer.Close()
	failingURL, highPriority := failingServer.URL, int64(10)
	failingChannel := model.Channel{Type: channeltype.Anthropic, Key: "failing-key", Name: "failing", Status: model.ChannelStatusEnabled, Group: "default", Models: "alias", BaseURL: &failingURL, Priority: &highPriority}
	if err := failingChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	config.RetryTimes = 1
	quotaBeforeRetry, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	retryRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alias","messages":[],"max_tokens":8,"future":{"preserve":true}}`))
	retryRequest.Header.Set("x-api-key", "sk-gateway-key")
	retryRequest.Header.Set("anthropic-beta", "test-beta")
	retryResponse := httptest.NewRecorder()
	r.ServeHTTP(retryResponse, retryRequest)
	if failureAttempts != 1 || retryResponse.Code != http.StatusOK || retryResponse.Body.String() != anthropicBody {
		t.Fatalf("native request did not retry once on a compatible channel: attempts %d status %d body %s", failureAttempts, retryResponse.Code, retryResponse.Body.String())
	}
	quotaAfterRetry, err := model.GetUserQuota(user.Id)
	if err != nil || quotaAfterRetry >= quotaBeforeRetry {
		t.Fatalf("retry was not billed: before %d after %d err %v", quotaBeforeRetry, quotaAfterRetry, err)
	}
	var retryLog model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id desc").First(&retryLog).Error; err != nil || retryLog.ChannelId != 1 || int64(retryLog.Quota) != quotaBeforeRetry-quotaAfterRetry {
		t.Fatalf("retry charged the wrong channel or quota: %+v err %v", retryLog, err)
	}

	config.RetryTimes = 0
	closedServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closedServer.URL
	closedServer.Close()
	legacyChannel := model.Channel{Type: channeltype.OpenAI, Key: "unused", Name: "legacy-failure", Status: model.ChannelStatusEnabled, Group: "default", Models: "legacy-failure", BaseURL: &closedURL}
	if err := legacyChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	quotaBeforeLegacyFailure, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	legacyRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"legacy-failure","messages":[{"role":"user","content":"hello"}]}`))
	legacyRequest.Header.Set("Content-Type", "application/json")
	legacyRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	legacyResponse := httptest.NewRecorder()
	r.ServeHTTP(legacyResponse, legacyRequest)
	quotaAfterLegacyFailure, err := model.GetUserQuota(user.Id)
	if legacyResponse.Code != http.StatusBadGateway || !strings.Contains(legacyResponse.Body.String(), "upstream request failed") || err != nil || quotaAfterLegacyFailure != quotaBeforeLegacyFailure {
		t.Fatalf("OpenAI Chat upstream failure leaked reserved quota: status %d body %s before %d after %d err %v", legacyResponse.Code, legacyResponse.Body.String(), quotaBeforeLegacyFailure, quotaAfterLegacyFailure, err)
	}
	chatNormal := `{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":6},"future":true}`
	chatEvents := "data: {\"id\":\"chat-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"future\":true}\n\n" +
		"data: {\"id\":\"chat-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6}}\n\n" +
		"data: [DONE]\n\n"
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer chat-provider-key" || r.Header.Get("OpenAI-Beta") != "test-beta" {
			t.Errorf("OpenAI Chat upstream route or credential changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"chat-upstream"`) || !strings.Contains(string(body), `"future":{"preserve":true}`) || !strings.Contains(string(body), `"image_url"`) {
			t.Errorf("OpenAI Chat lost request fields: %s", body)
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, chatEvents)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatNormal)
	}))
	defer chatServer.Close()
	chatURL, chatMapping := chatServer.URL, `{"chat-alias":"chat-upstream"}`
	chatChannel := model.Channel{Type: channeltype.OpenAI, Key: "chat-provider-key", Name: "chat", Status: model.ChannelStatusEnabled, Group: "default", Models: "chat-alias", BaseURL: &chatURL, ModelMapping: &chatMapping}
	if err := chatChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	chatInput := `{"model":"chat-alias","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}],"future":{"preserve":true}`
	for _, fixture := range []struct {
		suffix string
		want   string
	}{
		{`,"max_completion_tokens":32}`, chatNormal},
		{`,"stream":true,"max_completion_tokens":32,"stream_options":{"include_usage":true}}`, chatEvents},
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatInput+fixture.suffix))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer sk-gateway-key")
		request.Header.Set("OpenAI-Beta", "test-beta")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != fixture.want {
			t.Fatalf("OpenAI Chat passthrough changed: status %d body %s", response.Code, response.Body.String())
		}
	}
	var chatLogs []model.Log
	if err := db.Where("channel_id = ? AND type = ?", chatChannel.Id, model.LogTypeConsume).Find(&chatLogs).Error; err != nil || len(chatLogs) != 2 || chatLogs[0].PromptTokens != 4 || chatLogs[1].CompletionTokens != 6 || chatLogs[0].Quota != chatLogs[1].Quota {
		t.Fatalf("OpenAI Chat usage was not settled consistently: %+v err %v", chatLogs, err)
	}
	var bridgeCalls atomic.Int64
	bridgeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bridgeCalls.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "bridge-provider-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("Chat to Anthropic route or credentials changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"bridge-upstream"`) || !strings.Contains(string(body), `"system":"configured"`) {
			t.Errorf("Chat to Anthropic request lost fields: %s", body)
		}
		if strings.Contains(string(body), "upstream-fail") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"provider key rejected"}}`)
			return
		}
		if strings.Contains(string(body), "thinking-response") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_thinking","type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"private"}],"usage":{"input_tokens":5,"output_tokens":7}}`)
			return
		}
		if !strings.Contains(string(body), `"input_schema"`) || !strings.Contains(string(body), `"tool_use"`) || !strings.Contains(string(body), `"tool_result"`) {
			t.Errorf("Chat to Anthropic request lost tool fields: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_bridge","type":"message","stop_reason":"tool_use","content":[{"type":"text","text":"Searching"},{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"test"}}],"usage":{"input_tokens":5,"output_tokens":7}}`)
	}))
	defer bridgeServer.Close()
	bridgeURL, bridgeMapping, bridgePrompt := bridgeServer.URL, `{"bridge-alias":"bridge-upstream"}`, "configured"
	bridgeChannel := model.Channel{Type: channeltype.Anthropic, Key: "bridge-provider-key", Name: "bridge", Status: model.ChannelStatusEnabled, Group: "default", Models: "bridge-alias", BaseURL: &bridgeURL, ModelMapping: &bridgeMapping, SystemPrompt: &bridgePrompt}
	if err := bridgeChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	bridgeInput := `{"model":"bridge-alias","max_completion_tokens":24,"messages":[{"role":"system","content":"original"},{"role":"user","content":"find it"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"test\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"found"}],"tools":[{"type":"function","function":{"name":"search","description":"Search","parameters":{"type":"object"}}}]}`
	if !json.Valid([]byte(bridgeInput)) {
		t.Fatalf("invalid bridge fixture: %s", bridgeInput)
	}
	bridgeRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bridgeInput))
	bridgeRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	bridgeRequest.Header.Set("Content-Type", "application/json")
	bridgeResponse := httptest.NewRecorder()
	r.ServeHTTP(bridgeResponse, bridgeRequest)
	var chatResult struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if bridgeResponse.Code != http.StatusOK || json.Unmarshal(bridgeResponse.Body.Bytes(), &chatResult) != nil || chatResult.Object != "chat.completion" || chatResult.Model != "bridge-alias" || len(chatResult.Choices) != 1 || chatResult.Choices[0].FinishReason != "tool_calls" || len(chatResult.Choices[0].Message.ToolCalls) != 1 || chatResult.Usage.PromptTokens != 5 || chatResult.Usage.CompletionTokens != 7 {
		t.Fatalf("Chat to Anthropic response conversion failed: status %d body %s", bridgeResponse.Code, bridgeResponse.Body.String())
	}
	var bridgeLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", bridgeChannel.Id, model.LogTypeConsume).First(&bridgeLog).Error; err != nil || bridgeLog.PromptTokens != 5 || bridgeLog.CompletionTokens != 7 || !bridgeLog.SystemPromptReset {
		t.Fatalf("Chat to Anthropic quota was not settled: %+v err %v", bridgeLog, err)
	}
	for _, suffix := range []string{`,"response_format":{"type":"json_object"}}`} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.TrimSuffix(bridgeInput, "}")+suffix))
		request.Header.Set("Authorization", "Bearer sk-gateway-key")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity || bridgeCalls.Load() != 1 {
			t.Fatalf("unsupported Chat conversion reached upstream: status %d calls %d body %s", response.Code, bridgeCalls.Load(), response.Body.String())
		}
	}
	bridgeQuotaBeforeError, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	bridgeErrorRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-alias","messages":[{"role":"user","content":"upstream-fail"}],"max_tokens":24,"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]}`))
	bridgeErrorRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	bridgeErrorRequest.Header.Set("Content-Type", "application/json")
	bridgeErrorResponse := httptest.NewRecorder()
	r.ServeHTTP(bridgeErrorResponse, bridgeErrorRequest)
	bridgeQuotaAfterError, err := model.GetUserQuota(user.Id)
	if bridgeErrorResponse.Code != http.StatusUnauthorized || !strings.Contains(bridgeErrorResponse.Body.String(), `"type":"authentication_error"`) || !strings.Contains(bridgeErrorResponse.Body.String(), "provider key rejected") || bridgeQuotaAfterError != bridgeQuotaBeforeError || err != nil {
		t.Fatalf("Chat to Anthropic error mapping or refund failed: status %d body %s before %d after %d err %v", bridgeErrorResponse.Code, bridgeErrorResponse.Body.String(), bridgeQuotaBeforeError, bridgeQuotaAfterError, err)
	}
	bridgeThinkingRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-alias","messages":[{"role":"user","content":"thinking-response"}],"max_tokens":24}`))
	bridgeThinkingRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	bridgeThinkingRequest.Header.Set("Content-Type", "application/json")
	bridgeThinkingResponse := httptest.NewRecorder()
	r.ServeHTTP(bridgeThinkingResponse, bridgeThinkingRequest)
	var thinkingLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", bridgeChannel.Id, model.LogTypeConsume).Order("id desc").First(&thinkingLog).Error; err != nil || bridgeThinkingResponse.Code != http.StatusBadGateway || strings.Contains(bridgeThinkingResponse.Body.String(), "private") || !strings.Contains(thinkingLog.Content, "interrupted") {
		t.Fatalf("unrepresentable Anthropic thinking was exposed or unbilled: status %d body %s log %+v err %v", bridgeThinkingResponse.Code, bridgeThinkingResponse.Body.String(), thinkingLog, err)
	}
	var reverseCalls atomic.Int64
	reverseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reverseCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer reverse-provider-key" || r.Header.Get("x-api-key") != "" {
			t.Errorf("Messages to Chat route or credentials changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"reverse-upstream"`) || !strings.Contains(string(body), `"role":"system"`) || !strings.Contains(string(body), `"tool_calls"`) || !strings.Contains(string(body), `"tool_call_id":"toolu_1"`) {
			t.Errorf("Messages to Chat request lost fields: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat_reverse","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"Searching","tool_calls":[{"id":"call_2","type":"function","function":{"name":"search","arguments":"{\"q\":\"next\"}"}}]}}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":2}}}`)
	}))
	defer reverseServer.Close()
	reverseURL, reverseMapping := reverseServer.URL, `{"reverse-alias":"reverse-upstream"}`
	reverseChannel := model.Channel{Type: channeltype.OpenAI, Key: "reverse-provider-key", Name: "reverse", Status: model.ChannelStatusEnabled, Group: "default", Models: "reverse-alias", BaseURL: &reverseURL, ModelMapping: &reverseMapping}
	if err := reverseChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	reverseInput := `{"model":"reverse-alias","max_tokens":32,"system":"configured","messages":[{"role":"user","content":"find it"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"test"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"found"}]}],"tools":[{"name":"search","input_schema":{"type":"object"}}]}`
	reverseRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reverseInput))
	reverseRequest.Header.Set("x-api-key", "sk-gateway-key")
	reverseRequest.Header.Set("Content-Type", "application/json")
	reverseResponse := httptest.NewRecorder()
	r.ServeHTTP(reverseResponse, reverseRequest)
	var reverseResult struct {
		Model   string `json:"model"`
		Stop    string `json:"stop_reason"`
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"content"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Cached int64 `json:"cache_read_input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if reverseResponse.Code != http.StatusOK || json.Unmarshal(reverseResponse.Body.Bytes(), &reverseResult) != nil || reverseResult.Model != "reverse-alias" || reverseResult.Stop != "tool_use" || len(reverseResult.Content) != 2 || reverseResult.Content[1].ID != "call_2" || reverseResult.Usage.Input != 6 || reverseResult.Usage.Cached != 2 || reverseResult.Usage.Output != 3 {
		t.Fatalf("Messages to Chat response conversion failed: status %d body %s", reverseResponse.Code, reverseResponse.Body.String())
	}
	var reverseLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", reverseChannel.Id, model.LogTypeConsume).First(&reverseLog).Error; err != nil || reverseLog.PromptTokens != 8 || reverseLog.CompletionTokens != 3 {
		t.Fatalf("Messages to Chat usage was not settled: %+v err %v", reverseLog, err)
	}
	unsupportedReverse := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.TrimSuffix(reverseInput, "}")+`,"thinking":{"type":"enabled","budget_tokens":8}}`))
	unsupportedReverse.Header.Set("x-api-key", "sk-gateway-key")
	unsupportedReverse.Header.Set("Content-Type", "application/json")
	reverseUnsupportedResponse := httptest.NewRecorder()
	r.ServeHTTP(reverseUnsupportedResponse, unsupportedReverse)
	if reverseUnsupportedResponse.Code != http.StatusUnprocessableEntity || reverseCalls.Load() != 1 {
		t.Fatalf("unsupported Messages conversion reached upstream: status %d calls %d body %s", reverseUnsupportedResponse.Code, reverseCalls.Load(), reverseUnsupportedResponse.Body.String())
	}
	var geminiBridgeCalls atomic.Int64
	geminiBridgeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		geminiBridgeCalls.Add(1)
		if request.URL.Path != "/v1beta/models/gemini-bridge-upstream:generateContent" || request.Header.Get("x-goog-api-key") != "gemini-bridge-key" || request.Header.Get("Authorization") != "" {
			t.Errorf("Chat to Gemini route or credentials changed: %s %v", request.URL, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"systemInstruction":{"parts":[{"text":"configured"}]}`) || !strings.Contains(string(body), `"role":"user"`) || !strings.Contains(string(body), `"maxOutputTokens":16`) {
			t.Errorf("Chat to Gemini request lost fields: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"responseId":"gemini-bridge-response","modelVersion":"gemini-bridge-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP","safetyRatings":[]}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`)
	}))
	defer geminiBridgeServer.Close()
	geminiBridgeURL, geminiBridgeMapping, geminiBridgePrompt := geminiBridgeServer.URL, `{"gemini-bridge-alias":"gemini-bridge-upstream"}`, "configured"
	geminiBridgeChannel := model.Channel{Type: channeltype.Gemini, Key: "gemini-bridge-key", Name: "gemini-bridge", Status: model.ChannelStatusEnabled, Group: "default", Models: "gemini-bridge-alias", BaseURL: &geminiBridgeURL, ModelMapping: &geminiBridgeMapping, SystemPrompt: &geminiBridgePrompt}
	if err := geminiBridgeChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	geminiBridgeRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-bridge-alias","messages":[{"role":"system","content":"original"},{"role":"user","content":"hi"}],"max_tokens":16}`))
	geminiBridgeRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	geminiBridgeRequest.Header.Set("Content-Type", "application/json")
	geminiBridgeResponse := httptest.NewRecorder()
	r.ServeHTTP(geminiBridgeResponse, geminiBridgeRequest)
	if geminiBridgeResponse.Code != http.StatusOK || !strings.Contains(geminiBridgeResponse.Body.String(), `"model":"gemini-bridge-alias"`) || !strings.Contains(geminiBridgeResponse.Body.String(), `"completion_tokens":2`) || !strings.Contains(geminiBridgeResponse.Body.String(), `"modelVersion":"gemini-bridge-upstream"`) {
		t.Fatalf("Chat to Gemini response conversion failed: status %d body %s", geminiBridgeResponse.Code, geminiBridgeResponse.Body.String())
	}
	var geminiBridgeLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", geminiBridgeChannel.Id, model.LogTypeConsume).First(&geminiBridgeLog).Error; err != nil || geminiBridgeLog.PromptTokens != 4 || geminiBridgeLog.CompletionTokens != 2 || !geminiBridgeLog.SystemPromptReset {
		t.Fatalf("Chat to Gemini usage was not settled: %+v err %v", geminiBridgeLog, err)
	}
	unsupportedGeminiBridge := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-bridge-alias","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`))
	unsupportedGeminiBridge.Header.Set("Authorization", "Bearer sk-gateway-key")
	unsupportedGeminiBridge.Header.Set("Content-Type", "application/json")
	unsupportedGeminiBridgeResponse := httptest.NewRecorder()
	r.ServeHTTP(unsupportedGeminiBridgeResponse, unsupportedGeminiBridge)
	if unsupportedGeminiBridgeResponse.Code != http.StatusUnprocessableEntity || geminiBridgeCalls.Load() != 1 {
		t.Fatalf("unsupported Chat to Gemini request reached upstream: status %d calls %d body %s", unsupportedGeminiBridgeResponse.Code, geminiBridgeCalls.Load(), unsupportedGeminiBridgeResponse.Body.String())
	}
	completionNormal := `{"id":"cmpl-1","choices":[{"text":"ok"}],"usage":{"prompt_tokens":2,"completion_tokens":3},"future":true}`
	completionEvents := "data: {\"id\":\"cmpl-1\",\"choices\":[{\"text\":\"ok\"}],\"future\":true}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	completionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" || r.Header.Get("Authorization") != "Bearer completion-provider-key" {
			t.Errorf("Completions upstream route or credential changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"completion-upstream"`) || !strings.Contains(string(body), `"logprobs":3`) || !strings.Contains(string(body), `"future":{"preserve":true}`) {
			t.Errorf("Completions request lost fields: %s", body)
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, completionEvents)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completionNormal)
	}))
	defer completionServer.Close()
	completionURL, completionMapping := completionServer.URL, `{"completion-alias":"completion-upstream"}`
	completionChannel := model.Channel{Type: channeltype.OpenAI, Key: "completion-provider-key", Name: "completion", Status: model.ChannelStatusEnabled, Group: "default", Models: "completion-alias", BaseURL: &completionURL, ModelMapping: &completionMapping}
	if err := completionChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		stream bool
		want   string
	}{
		{false, completionNormal},
		{true, completionEvents},
	} {
		body := `{"model":"completion-alias","prompt":"hello","max_tokens":8,"logprobs":3,"future":{"preserve":true}`
		if fixture.stream {
			body += `,"stream":true,"stream_options":{"include_usage":true}`
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body+`}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer sk-gateway-key")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != fixture.want {
			t.Fatalf("Completions passthrough changed: status %d body %s", response.Code, response.Body.String())
		}
	}
	var completionLogs []model.Log
	if err := db.Where("channel_id = ? AND type = ?", completionChannel.Id, model.LogTypeConsume).Find(&completionLogs).Error; err != nil || len(completionLogs) != 2 || completionLogs[0].PromptTokens != 2 || completionLogs[1].CompletionTokens != 3 {
		t.Fatalf("Completions usage was not settled: %+v err %v", completionLogs, err)
	}
	embeddingBody := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"embedding-upstream","usage":{"prompt_tokens":7,"total_tokens":7},"future":true}`
	embeddingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer embedding-provider-key" {
			t.Errorf("Embeddings upstream route or credential changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"embedding-upstream"`) || !strings.Contains(string(body), `"dimensions":2`) || !strings.Contains(string(body), `"future":{"preserve":true}`) {
			t.Errorf("Embeddings request lost fields: %s", body)
		}
		if strings.Contains(string(body), `"fail":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, embeddingBody)
	}))
	defer embeddingServer.Close()
	embeddingURL, embeddingMapping := embeddingServer.URL, `{"embedding-alias":"embedding-upstream"}`
	embeddingChannel := model.Channel{Type: channeltype.OpenAI, Key: "embedding-provider-key", Name: "embedding", Status: model.ChannelStatusEnabled, Group: "default", Models: "embedding-alias", BaseURL: &embeddingURL, ModelMapping: &embeddingMapping}
	if err := embeddingChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	embeddingRequest := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embedding-alias","input":["hello","world"],"dimensions":2,"future":{"preserve":true}}`))
	embeddingRequest.Header.Set("Content-Type", "application/json")
	embeddingRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	embeddingResponse := httptest.NewRecorder()
	r.ServeHTTP(embeddingResponse, embeddingRequest)
	if embeddingResponse.Code != http.StatusOK || embeddingResponse.Body.String() != embeddingBody {
		t.Fatalf("Embeddings passthrough changed: status %d body %s", embeddingResponse.Code, embeddingResponse.Body.String())
	}
	var embeddingLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", embeddingChannel.Id, model.LogTypeConsume).First(&embeddingLog).Error; err != nil || embeddingLog.PromptTokens != 7 || embeddingLog.CompletionTokens != 0 {
		t.Fatalf("Embeddings usage was not settled: %+v err %v", embeddingLog, err)
	}
	engineRequest := httptest.NewRequest(http.MethodPost, "/v1/engines/embedding-alias/embeddings", strings.NewReader(`{"input":["hello","world"],"dimensions":2,"future":{"preserve":true}}`))
	engineRequest.Header.Set("Content-Type", "application/json")
	engineRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	engineResponse := httptest.NewRecorder()
	r.ServeHTTP(engineResponse, engineRequest)
	if engineResponse.Code != http.StatusOK || engineResponse.Body.String() != embeddingBody {
		t.Fatalf("engine Embeddings compatibility changed: status %d body %s", engineResponse.Code, engineResponse.Body.String())
	}
	engineMismatch := httptest.NewRequest(http.MethodPost, "/v1/engines/embedding-alias/embeddings", strings.NewReader(`{"model":"different","input":"hello"}`))
	engineMismatch.Header.Set("Content-Type", "application/json")
	engineMismatch.Header.Set("Authorization", "Bearer sk-gateway-key")
	engineMismatchResponse := httptest.NewRecorder()
	r.ServeHTTP(engineMismatchResponse, engineMismatch)
	if engineMismatchResponse.Code != http.StatusBadRequest {
		t.Fatalf("engine path/body model mismatch was accepted: status %d body %s", engineMismatchResponse.Code, engineMismatchResponse.Body.String())
	}
	quotaBeforeEmbeddingError, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	embeddingErrorRequest := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embedding-alias","input":"hello","dimensions":2,"future":{"preserve":true},"fail":true}`))
	embeddingErrorRequest.Header.Set("Content-Type", "application/json")
	embeddingErrorRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	embeddingErrorResponse := httptest.NewRecorder()
	r.ServeHTTP(embeddingErrorResponse, embeddingErrorRequest)
	quotaAfterEmbeddingError, err := model.GetUserQuota(user.Id)
	if embeddingErrorResponse.Code != http.StatusTooManyRequests || quotaAfterEmbeddingError != quotaBeforeEmbeddingError || err != nil {
		t.Fatalf("Embeddings upstream error was billed: status %d before %d after %d err %v", embeddingErrorResponse.Code, quotaBeforeEmbeddingError, quotaAfterEmbeddingError, err)
	}

	moderationBody := `{"id":"modr-1","model":"moderation-upstream","results":[{"flagged":false,"category_applied_input_types":{"violence":["text"]}}],"future":true}`
	moderationWithUsage := `{"id":"modr-2","results":[{"flagged":false}],"usage":{"input_tokens":12},"future":true}`
	moderationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/moderations" || r.Header.Get("Authorization") != "Bearer moderation-provider-key" {
			t.Errorf("Moderations upstream route or credential changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"moderation-upstream"`) || !strings.Contains(string(body), `"future":{"preserve":true}`) {
			t.Errorf("Moderations request lost fields: %s", body)
		}
		if strings.Contains(string(body), `"fail":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		if strings.Contains(string(body), `"reported":true`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, moderationWithUsage)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, moderationBody)
	}))
	defer moderationServer.Close()
	moderationURL, moderationMapping := moderationServer.URL, `{"moderation-alias":"moderation-upstream","omni-moderation-latest":"moderation-upstream"}`
	moderationPrompt := "should not be added to Moderations"
	moderationChannel := model.Channel{Type: channeltype.OpenAI, Key: "moderation-provider-key", Name: "moderation", Status: model.ChannelStatusEnabled, Group: "default", Models: "moderation-alias,omni-moderation-latest", BaseURL: &moderationURL, ModelMapping: &moderationMapping, SystemPrompt: &moderationPrompt}
	if err := moderationChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"model":"moderation-alias","input":"hello world","future":{"preserve":true}}`,
		`{"input":[{"type":"text","text":"hello world"}],"future":{"preserve":true}}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer sk-gateway-key")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != moderationBody {
			t.Fatalf("Moderations passthrough changed: status %d body %s", response.Code, response.Body.String())
		}
	}
	var moderationLogs []model.Log
	if err := db.Where("channel_id = ? AND type = ?", moderationChannel.Id, model.LogTypeConsume).Find(&moderationLogs).Error; err != nil || len(moderationLogs) != 2 || moderationLogs[0].PromptTokens == 0 || moderationLogs[1].PromptTokens == 0 || moderationLogs[0].SystemPromptReset {
		t.Fatalf("Moderations fallback usage was not settled: %+v err %v", moderationLogs, err)
	}
	moderationReportedRequest := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"model":"moderation-alias","input":"hello","future":{"preserve":true},"reported":true}`))
	moderationReportedRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	moderationReportedResponse := httptest.NewRecorder()
	r.ServeHTTP(moderationReportedResponse, moderationReportedRequest)
	if moderationReportedResponse.Code != http.StatusOK || moderationReportedResponse.Body.String() != moderationWithUsage {
		t.Fatalf("Moderations provider usage response changed: status %d body %s", moderationReportedResponse.Code, moderationReportedResponse.Body.String())
	}
	var reportedLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", moderationChannel.Id, model.LogTypeConsume).Order("id desc").First(&reportedLog).Error; err != nil || reportedLog.PromptTokens != 12 || reportedLog.SystemPromptReset {
		t.Fatalf("Moderations provider usage did not take precedence: %+v err %v", reportedLog, err)
	}
	moderationImageRequest := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"input":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}],"future":{"preserve":true}}`))
	moderationImageRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	moderationImageResponse := httptest.NewRecorder()
	r.ServeHTTP(moderationImageResponse, moderationImageRequest)
	if moderationImageResponse.Code != http.StatusOK || moderationImageResponse.Body.String() != moderationBody {
		t.Fatalf("Moderations image input changed: status %d body %s", moderationImageResponse.Code, moderationImageResponse.Body.String())
	}
	var imageLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", moderationChannel.Id, model.LogTypeConsume).Order("id desc").First(&imageLog).Error; err != nil || imageLog.PromptTokens != 0 || !strings.Contains(imageLog.Content, "upstream usage unavailable") {
		t.Fatalf("Moderations image input fabricated text usage: %+v err %v", imageLog, err)
	}
	quotaBeforeModerationError, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	moderationErrorRequest := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"model":"moderation-alias","input":"hello","future":{"preserve":true},"fail":true}`))
	moderationErrorRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	moderationErrorResponse := httptest.NewRecorder()
	r.ServeHTTP(moderationErrorResponse, moderationErrorRequest)
	quotaAfterModerationError, err := model.GetUserQuota(user.Id)
	if moderationErrorResponse.Code != http.StatusTooManyRequests || quotaAfterModerationError != quotaBeforeModerationError || err != nil {
		t.Fatalf("Moderations upstream error was billed: status %d before %d after %d err %v", moderationErrorResponse.Code, quotaBeforeModerationError, quotaAfterModerationError, err)
	}
	moderationInvalidRequest := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"model":`))
	moderationInvalidRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	moderationInvalidResponse := httptest.NewRecorder()
	r.ServeHTTP(moderationInvalidResponse, moderationInvalidRequest)
	if moderationInvalidResponse.Code != http.StatusBadRequest || !strings.Contains(moderationInvalidResponse.Body.String(), `"type":"invalid_request_error"`) {
		t.Fatalf("invalid Moderations JSON did not return an OpenAI error: status %d body %s", moderationInvalidResponse.Code, moderationInvalidResponse.Body.String())
	}
	moderationUnauthorizedRequest := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"model":"moderation-alias","input":"hello"}`))
	moderationUnauthorizedResponse := httptest.NewRecorder()
	r.ServeHTTP(moderationUnauthorizedResponse, moderationUnauthorizedRequest)
	if moderationUnauthorizedResponse.Code != http.StatusUnauthorized || !strings.Contains(moderationUnauthorizedResponse.Body.String(), `"type":"authentication_error"`) {
		t.Fatalf("Moderations authentication error envelope changed: status %d body %s", moderationUnauthorizedResponse.Code, moderationUnauthorizedResponse.Body.String())
	}

	var redirected atomic.Bool
	redirectSink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer redirectSink.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectSink.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectSource.Close()
	redirectURL := redirectSource.URL
	redirectChannel := model.Channel{Type: channeltype.Anthropic, Key: "provider-secret", Name: "redirect-source", Status: model.ChannelStatusEnabled, Group: "default", Models: "redirect-only", BaseURL: &redirectURL}
	if err := redirectChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	quotaBeforeRedirect, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	redirectRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"redirect-only","messages":[],"max_tokens":8}`))
	redirectRequest.Header.Set("x-api-key", "sk-gateway-key")
	redirectResponse := httptest.NewRecorder()
	r.ServeHTTP(redirectResponse, redirectRequest)
	quotaAfterRedirect, err := model.GetUserQuota(user.Id)
	if redirectResponse.Code != http.StatusBadGateway || redirected.Load() || err != nil || quotaAfterRedirect != quotaBeforeRedirect {
		t.Fatalf("provider redirect was unsafe or billed: status %d redirected %t before %d after %d err %v", redirectResponse.Code, redirected.Load(), quotaBeforeRedirect, quotaAfterRedirect, err)
	}

	responsesNormal := `{"object":"response","status":"completed","usage":{"input_tokens":4,"output_tokens":6},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"future":true}`
	responsesEvents := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\",\"future\":true}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":6}}}\n\n"
	var responsesUpstreamCalls atomic.Int64
	responsesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responsesUpstreamCalls.Add(1)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer responses-provider-key" || r.Header.Get("x-api-key") != "" {
			t.Errorf("Responses upstream route or credential changed: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"responses-upstream"`) || !strings.Contains(string(body), `"instructions":"configured"`) || !strings.Contains(string(body), `"future":{"preserve":true}`) || !strings.Contains(string(body), `"input_image"`) {
			t.Errorf("Responses upstream lost native fields: %s", body)
		}
		if strings.Contains(string(body), `"fail":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		w.Header().Set("X-Request-Id", "responses-test-id")
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, responsesEvents)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responsesNormal)
	}))
	defer responsesServer.Close()
	responsesURL, responsesMapping, responsesPrompt := responsesServer.URL, `{"responses-alias":"responses-upstream"}`, "configured"
	responsesChannel := model.Channel{Type: channeltype.OpenAI, Key: "responses-provider-key", Name: "responses", Status: model.ChannelStatusEnabled, Group: "default", Models: "responses-alias", BaseURL: &responsesURL, ModelMapping: &responsesMapping, SystemPrompt: &responsesPrompt}
	if err := responsesChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	responsesInput := `{"model":"responses-alias","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}],"future":{"preserve":true}`
	quotaBeforeResponses, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		suffix string
		want   string
	}{
		{`,"max_output_tokens":32}`, responsesNormal},
		{`,"stream":true,"max_output_tokens":32}`, responsesEvents},
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responsesInput+fixture.suffix))
		request.Header.Set("Authorization", "Bearer sk-gateway-key")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != fixture.want || response.Header().Get("X-Request-Id") != "responses-test-id" {
			t.Fatalf("Responses route output changed: status %d body %s", response.Code, response.Body.String())
		}
	}
	quotaAfterResponses, err := model.GetUserQuota(user.Id)
	if err != nil || quotaAfterResponses >= quotaBeforeResponses || responsesUpstreamCalls.Load() != 2 {
		t.Fatalf("Responses route did not settle twice: before %d after %d upstream calls %d err %v", quotaBeforeResponses, quotaAfterResponses, responsesUpstreamCalls.Load(), err)
	}
	var responsesLogs []model.Log
	if err := db.Where("channel_id = ? AND type = ?", responsesChannel.Id, model.LogTypeConsume).Find(&responsesLogs).Error; err != nil || len(responsesLogs) != 2 || !responsesLogs[0].SystemPromptReset || !responsesLogs[1].SystemPromptReset {
		t.Fatalf("Responses quota logs are wrong: %+v err %v", responsesLogs, err)
	}
	backgroundRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responsesInput+`,"background":true}`))
	backgroundRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	backgroundResponse := httptest.NewRecorder()
	r.ServeHTTP(backgroundResponse, backgroundRequest)
	if backgroundResponse.Code != http.StatusAccepted || !strings.Contains(backgroundResponse.Body.String(), `"status":"queued"`) {
		t.Fatalf("background response create failed: status %d body %s", backgroundResponse.Code, backgroundResponse.Body.String())
	}
	var backgroundCreated struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(backgroundResponse.Body.Bytes(), &backgroundCreated); err != nil || backgroundCreated.ID == "" {
		t.Fatalf("background response id missing: %s err %v", backgroundResponse.Body.String(), err)
	}
	var backgroundResult struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		retrieveRequest := httptest.NewRequest(http.MethodGet, "/v1/responses/"+backgroundCreated.ID, nil)
		retrieveRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
		retrieveResponse := httptest.NewRecorder()
		r.ServeHTTP(retrieveResponse, retrieveRequest)
		if retrieveResponse.Code != http.StatusOK {
			t.Fatalf("background retrieval failed: status %d body %s", retrieveResponse.Code, retrieveResponse.Body.String())
		}
		if err := json.Unmarshal(retrieveResponse.Body.Bytes(), &backgroundResult); err != nil {
			t.Fatalf("background retrieval unparseable: %s", retrieveResponse.Body.String())
		}
		if backgroundResult.Status != "queued" && backgroundResult.Status != "in_progress" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background response never completed: %s", retrieveResponse.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if backgroundResult.ID != backgroundCreated.ID || backgroundResult.Status != "completed" || len(backgroundResult.Output) == 0 || backgroundResult.Output[0].Content[0].Text != "ok" {
		t.Fatalf("background response result wrong: %+v", backgroundResult)
	}
	// wait for the background settlement to land
	time.Sleep(100 * time.Millisecond)
	quotaAfterBackground, err := model.GetUserQuota(user.Id)
	if err != nil || quotaAfterBackground >= quotaAfterResponses || responsesUpstreamCalls.Load() != 3 {
		t.Fatalf("background response did not settle: before %d after %d calls %d err %v", quotaAfterResponses, quotaAfterBackground, responsesUpstreamCalls.Load(), err)
	}
	responsesFailureRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responsesInput+`,"fail":true}`))
	responsesFailureRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	responsesFailureResponse := httptest.NewRecorder()
	r.ServeHTTP(responsesFailureResponse, responsesFailureRequest)
	quotaAfterResponsesError, err := model.GetUserQuota(user.Id)
	if responsesFailureResponse.Code != http.StatusTooManyRequests || !strings.Contains(responsesFailureResponse.Body.String(), `"rate_limit_error"`) || err != nil || quotaAfterResponsesError != quotaAfterBackground {
		t.Fatalf("Responses upstream error changed or billed: status %d body %s quota %d err %v", responsesFailureResponse.Code, responsesFailureResponse.Body.String(), quotaAfterResponsesError, err)
	}
	cancelUnknown := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_unknown/cancel", nil)
	cancelUnknown.Header.Set("Authorization", "Bearer sk-gateway-key")
	cancelUnknownResponse := httptest.NewRecorder()
	r.ServeHTTP(cancelUnknownResponse, cancelUnknown)
	if cancelUnknownResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown background cancel changed: status %d body %s", cancelUnknownResponse.Code, cancelUnknownResponse.Body.String())
	}
}

type firstWriteRecorder struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
}

func (w *firstWriteRecorder) Write(p []byte) (int, error) {
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return w.ResponseRecorder.Write(p)
}
