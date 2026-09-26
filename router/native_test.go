package router

import (
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

func TestNativeRoutesSelectMatchingProtocolAndPreserveWireData(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := client.HTTPClient
	previousRedis, previousMemoryCache := common.RedisEnabled, config.MemoryCacheEnabled
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
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		client.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled = previousRedis, previousMemoryCache
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
	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-provider-key" || r.Header.Get("anthropic-beta") != "test-beta" {
			t.Errorf("Anthropic upstream got incorrect route or headers: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"fail":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		if !strings.Contains(string(body), `"model":"claude-upstream"`) || !strings.Contains(string(body), `"future":{"preserve":true}`) {
			t.Errorf("Anthropic upstream lost model mapping or extension: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicBody)
	}))
	defer anthropicServer.Close()
	geminiEvents := "data: {\"candidates\":[],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":5},\"newField\":true}\n\n"
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-upstream:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" || r.Header.Get("x-goog-api-key") != "gemini-provider-key" {
			t.Errorf("Gemini upstream got incorrect route or headers: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
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
}
