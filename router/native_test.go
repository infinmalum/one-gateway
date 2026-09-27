package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	user := model.User{Username: "native-test", Status: model.UserStatusEnabled, Group: "default", Quota: 1000000}
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
	unsupportedRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"openai-only","messages":[],"max_tokens":8}`))
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
	if legacyResponse.Code != http.StatusInternalServerError || !strings.Contains(legacyResponse.Body.String(), "do_request_failed") || err != nil || quotaAfterLegacyFailure != quotaBeforeLegacyFailure {
		t.Fatalf("legacy upstream failure leaked reserved quota: status %d body %s before %d after %d err %v", legacyResponse.Code, legacyResponse.Body.String(), quotaBeforeLegacyFailure, quotaAfterLegacyFailure, err)
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
	if backgroundResponse.Code != http.StatusUnprocessableEntity || responsesUpstreamCalls.Load() != 2 {
		t.Fatalf("unsupported background response reached upstream: status %d calls %d", backgroundResponse.Code, responsesUpstreamCalls.Load())
	}
	responsesFailureRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responsesInput+`,"fail":true}`))
	responsesFailureRequest.Header.Set("Authorization", "Bearer sk-gateway-key")
	responsesFailureResponse := httptest.NewRecorder()
	r.ServeHTTP(responsesFailureResponse, responsesFailureRequest)
	quotaAfterResponsesError, err := model.GetUserQuota(user.Id)
	if responsesFailureResponse.Code != http.StatusTooManyRequests || !strings.Contains(responsesFailureResponse.Body.String(), `"rate_limit_error"`) || err != nil || quotaAfterResponsesError != quotaAfterResponses {
		t.Fatalf("Responses upstream error changed or billed: status %d body %s quota %d err %v", responsesFailureResponse.Code, responsesFailureResponse.Body.String(), quotaAfterResponsesError, err)
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
