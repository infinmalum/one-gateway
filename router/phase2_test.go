package router

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

type notifyingRecorder struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
	once  sync.Once
}

func (w *notifyingRecorder) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.once.Do(func() { close(w.wrote) })
	return n, err
}

func TestLegacyImageAndAudioSettleOnlySuccessfulChannel(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := client.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:phase2-legacy?mode=memory&cache=shared"), &gorm.Config{})
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
	user := model.User{Username: "phase2", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "phase2-key", Name: "phase2", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Errorf("wrong upstream credential: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "cancel audio") {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "partial-audio")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if strings.Contains(string(body), "fail") {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/images/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://example.com/image.png"}]}`)
			return
		}
		if strings.Contains(r.URL.Path, "/audio/speech") {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "audio-bytes")
			return
		}
		if strings.Contains(r.URL.Path, "/audio/transcriptions") || strings.Contains(r.URL.Path, "/audio/translations") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"text":"hello"}`)
			return
		}
		t.Errorf("unexpected upstream route: %s", r.URL)
	}))
	defer upstream.Close()
	baseURL := upstream.URL
	channel := model.Channel{Type: channeltype.OpenAI, Key: "upstream-key", Name: "phase2-channel", Status: model.ChannelStatusEnabled, Group: "default", Models: "dall-e-2,tts-1,whisper-1", BaseURL: &baseURL}
	if err := channel.Insert(); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetRelayRouter(router)
	fixtures := []struct {
		path   string
		body   string
		status int
	}{
		{"/v1/images/generations", `{"model":"dall-e-2","prompt":"fail"}`, http.StatusTooManyRequests},
		{"/v1/images/generations", `{"model":"dall-e-2","prompt":"draw a tree"}`, http.StatusOK},
		{"/v1/audio/speech", `{"model":"tts-1","input":"fail","voice":"alloy"}`, http.StatusTooManyRequests},
		{"/v1/audio/speech", `{"model":"tts-1","input":"hello","voice":"alloy"}`, http.StatusOK},
	}
	for _, fixture := range fixtures {
		request := httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer sk-phase2-key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != fixture.status {
			t.Fatalf("%s: status %d body %s", fixture.path, response.Code, response.Body.String())
		}
	}
	var logs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected two successful usage logs, got %d", len(logs))
	}
	for _, log := range logs {
		if log.ChannelId != channel.Id || log.Quota <= 0 {
			t.Fatalf("incorrect channel or quota in log: %+v", log)
		}
	}
	quota, err := model.GetUserQuota(user.Id)
	if err != nil || quota != user.Quota-int64(logs[0].Quota+logs[1].Quota) {
		t.Fatalf("quota and logs differ: quota %d logs %+v error %v", quota, logs, err)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"retry me"}}`)
	}))
	defer failing.Close()
	succeeding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://example.com/retried.png"}]}`)
	}))
	defer succeeding.Close()
	high, low := int64(10), int64(0)
	failingURL, succeedingURL := failing.URL, succeeding.URL
	failedChannel := model.Channel{Type: channeltype.OpenAI, Key: "upstream-key", Name: "retry-first", Status: model.ChannelStatusEnabled, Group: "default", Models: "retry-image", BaseURL: &failingURL, Priority: &high}
	successChannel := model.Channel{Type: channeltype.OpenAI, Key: "upstream-key", Name: "retry-second", Status: model.ChannelStatusEnabled, Group: "default", Models: "retry-image", BaseURL: &succeedingURL, Priority: &low}
	for _, candidate := range []*model.Channel{&failedChannel, &successChannel} {
		if err := candidate.Insert(); err != nil {
			t.Fatal(err)
		}
	}
	config.RetryTimes = 1
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"retry-image","prompt":"a tree"}`))
	request.Header.Set("Authorization", "Bearer sk-phase2-key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "retried.png") {
		t.Fatalf("image retry: status %d body %s", response.Code, response.Body.String())
	}
	var retryLogs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&retryLogs).Error; err != nil || len(retryLogs) != 3 || retryLogs[2].ChannelId != successChannel.Id {
		t.Fatalf("retry must settle once on successful channel: logs %+v error %v", retryLogs, err)
	}
	finalQuota, err := model.GetUserQuota(user.Id)
	if err != nil || finalQuota != quota-int64(retryLogs[2].Quota) {
		t.Fatalf("retry quota mismatch: before %d after %d log %+v error %v", quota, finalQuota, retryLogs[2], err)
	}
	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/completions", "/v1/edits":
				_, _ = io.WriteString(w, `{"choices":[{"text":"ok"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
			case "/v1/embeddings":
				_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
			case "/v1/moderations":
				_, _ = io.WriteString(w, `{"results":[{"flagged":false}]}`)
			default:
				t.Errorf("unexpected legacy text route: %s", r.URL)
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			if !strings.Contains(string(body), "truncate") {
				_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	defer chatUpstream.Close()
	chatURL := chatUpstream.URL
	chatChannel := model.Channel{Type: channeltype.OpenRouter, Key: "upstream-key", Name: "legacy-chat", Status: model.ChannelStatusEnabled, Group: "default", Models: "legacy-chat,legacy-completion,legacy-embed,legacy-edit,legacy-moderation", BaseURL: &chatURL}
	if err := chatChannel.Insert(); err != nil {
		t.Fatal(err)
	}
	chatRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"legacy-chat","messages":[{"role":"user","content":"hello"}]}`))
	chatRequest.Header.Set("Authorization", "Bearer sk-phase2-key")
	chatRequest.Header.Set("Content-Type", "application/json")
	chatResponse := httptest.NewRecorder()
	router.ServeHTTP(chatResponse, chatRequest)
	if chatResponse.Code != http.StatusOK || !strings.Contains(chatResponse.Body.String(), `"content":"ok"`) {
		t.Fatalf("legacy Chat response: status %d body %s", chatResponse.Code, chatResponse.Body.String())
	}
	var chatLogs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&chatLogs).Error; err != nil || len(chatLogs) != 4 || chatLogs[3].ChannelId != chatChannel.Id {
		t.Fatalf("legacy Chat must settle on selected channel: logs %+v error %v", chatLogs, err)
	}
	for _, fixture := range []struct {
		body string
		done bool
	}{
		{`{"model":"legacy-chat","messages":[{"role":"user","content":"complete"}],"stream":true}`, true},
		{`{"model":"legacy-chat","messages":[{"role":"user","content":"truncate"}],"stream":true}`, false},
	} {
		streamRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fixture.body))
		streamRequest.Header.Set("Authorization", "Bearer sk-phase2-key")
		streamRequest.Header.Set("Content-Type", "application/json")
		streamResponse := httptest.NewRecorder()
		router.ServeHTTP(streamResponse, streamRequest)
		if streamResponse.Code != http.StatusOK || strings.Contains(streamResponse.Body.String(), "[DONE]") != fixture.done {
			t.Fatalf("legacy Chat stream termination changed: body %s", streamResponse.Body.String())
		}
	}
	var streamLogs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&streamLogs).Error; err != nil || len(streamLogs) != 6 || streamLogs[4].ChannelId != chatChannel.Id || streamLogs[5].ChannelId != chatChannel.Id || streamLogs[5].Quota <= 0 {
		t.Fatalf("legacy Chat streams must settle exactly once: logs %+v error %v", streamLogs, err)
	}
	for _, fixture := range []struct {
		path string
		body string
	}{
		{"/v1/completions", `{"model":"legacy-completion","prompt":"hello"}`},
		{"/v1/embeddings", `{"model":"legacy-embed","input":"hello"}`},
		{"/v1/edits", `{"model":"legacy-edit","instruction":"improve","input":"hello"}`},
		{"/v1/moderations", `{"model":"legacy-moderation","input":"hello"}`},
	} {
		request := httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body))
		request.Header.Set("Authorization", "Bearer sk-phase2-key")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("legacy %s: status %d body %s", fixture.path, response.Code, response.Body.String())
		}
	}
	var textLogs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&textLogs).Error; err != nil || len(textLogs) != 10 {
		t.Fatalf("legacy text routes must each settle once: logs %+v error %v", textLogs, err)
	}
	for _, log := range textLogs[6:] {
		if log.ChannelId != chatChannel.Id || log.Quota <= 0 {
			t.Fatalf("legacy text settled wrong channel or quota: %+v", log)
		}
	}
	cancelContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelRequest := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"tts-1","input":"cancel audio","voice":"alloy"}`)).WithContext(cancelContext)
	cancelRequest.Header.Set("Authorization", "Bearer sk-phase2-key")
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelResponse := &notifyingRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	go func() {
		select {
		case <-cancelResponse.wrote:
		case <-time.After(3 * time.Second):
		}
		cancel()
	}()
	router.ServeHTTP(cancelResponse, cancelRequest)
	var cancellationLogs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&cancellationLogs).Error; err != nil || len(cancellationLogs) != 11 || cancellationLogs[10].ChannelId != channel.Id || cancellationLogs[10].Quota <= 0 {
		t.Fatalf("cancelled audio must settle once on selected channel: logs %+v error %v", cancellationLogs, err)
	}
	for _, path := range []string{"/v1/audio/transcriptions", "/v1/audio/translations"} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("model", "whisper-1"); err != nil {
			t.Fatal(err)
		}
		file, err := writer.CreateFormFile("file", "sample.wav")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, "sample-audio"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, &body)
		request.Header.Set("Authorization", "Bearer sk-phase2-key")
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"text":"hello"`) {
			t.Fatalf("%s: status %d body %s", path, response.Code, response.Body.String())
		}
	}
	var audioLogs []model.Log
	if err := db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&audioLogs).Error; err != nil || len(audioLogs) != 13 || audioLogs[11].ChannelId != channel.Id || audioLogs[12].ChannelId != channel.Id {
		t.Fatalf("multipart audio must settle once per request: logs %+v error %v", audioLogs, err)
	}
	proxyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" || r.Header.Get("Authorization") != "proxy-key" {
			t.Errorf("proxy selected wrong target or credential: %s %q", r.URL, r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, "healthy")
	}))
	defer proxyUpstream.Close()
	proxyURL := proxyUpstream.URL
	proxyChannel := model.Channel{Type: channeltype.Proxy, Key: "proxy-key", Name: "proxy", Status: model.ChannelStatusEnabled, Group: "default", BaseURL: &proxyURL}
	if err := db.Create(&proxyChannel).Error; err != nil {
		t.Fatal(err)
	}
	proxyRequest := httptest.NewRequest(http.MethodGet, "/v1/oneapi/proxy/"+strconv.Itoa(proxyChannel.Id)+"/health", nil)
	proxyRequest.Header.Set("Authorization", "Bearer sk-phase2-key")
	proxyResponse := httptest.NewRecorder()
	router.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK || proxyResponse.Body.String() != "healthy" {
		t.Fatalf("proxy response: status %d body %s", proxyResponse.Code, proxyResponse.Body.String())
	}
	var logCount int64
	if err := db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logCount).Error; err != nil || logCount != 13 {
		t.Fatalf("unmetered proxy must not create a usage charge: count %d error %v", logCount, err)
	}
}
