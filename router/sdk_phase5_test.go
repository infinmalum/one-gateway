package router

import (
	"bytes"
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
	commonclient "github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/model"
	billingratio "github.com/infinmalum/one-gateway/relay/billing/ratio"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The official OpenAI client and local provider fixtures cover the background
// Responses lifecycle and the fixed-quota operations migrated in phase 5. No
// external API credentials or live provider access are involved.
func TestPhase5BackgroundResponsesAndOperations(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := commonclient.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:phase5-sdk?mode=memory&cache=shared"), &gorm.Config{})
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
	commonclient.HTTPClient = &http.Client{}
	common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite = false, false, true
	config.RetryTimes = 0
	// Released in cleanup so the blocking provider fixture cannot outlive the
	// test even when an assertion fails mid-flight.
	holdRelease := make(chan struct{})
	responsesStarted := make(chan struct{}, 8)
	t.Cleanup(func() {
		close(holdRelease)
		model.DB, model.LOG_DB = previousDB, previousLogDB
		commonclient.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes = previousRedis, previousMemoryCache, previousRetries
		common.UsingSQLite = previousSQLite
		_ = sqlDB.Close()
	})
	user := model.User{Username: "phase5-sdk", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "phase5-gateway-key", Name: "phase5-sdk", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	// The Responses provider blocks requests carrying the "hold" extension
	// until the test releases them, so cancellation can be observed mid-flight.
	responsesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer phase5-responses-key" {
			t.Errorf("Responses provider request: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"hold":true`) {
			responsesStarted <- struct{}{}
			select {
			case <-holdRelease:
			case <-r.Context().Done():
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"held"}]}]}`)
			return
		}
		if strings.Contains(string(body), `"fail":true`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"phase5 busy"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","status":"completed","usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"bg ok"}]}],"future":true}`)
	}))
	defer responsesServer.Close()

	imagesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer phase5-images-key" {
			t.Errorf("Images provider request: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"dall-e-3"`) || !strings.Contains(string(body), `"prompt":"a tree"`) || !strings.Contains(string(body), `"quality":"hd"`) {
			t.Errorf("Images provider lost fields: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://example.test/tree.png"}]}`)
	}))
	defer imagesServer.Close()

	speechServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer phase5-speech-key" {
			t.Errorf("Speech provider request: %s %v", r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"phase5-tts"`) || !strings.Contains(string(body), `"input":"hello audio"`) {
			t.Errorf("Speech provider lost fields: %s", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "\x01\x02\x03binaryaudio")
	}))
	defer speechServer.Close()

	transcriptionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer phase5-whisper-key" {
			t.Errorf("Transcription provider request: %s %v", r.URL, r.Header)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("Transcription provider lost the multipart content type: %s", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("Transcription multipart parse: %v", err)
			return
		}
		if r.FormValue("model") != "whisper-1" {
			t.Errorf("Transcription provider lost the model form field: %s", r.FormValue("model"))
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("Transcription provider lost the file: %v", err)
			return
		}
		defer file.Close()
		audio, _ := io.ReadAll(file)
		if string(audio) != "RIFFwavdata" {
			t.Errorf("Transcription audio bytes changed: %q", audio)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello transcript"}`)
	}))
	defer transcriptionServer.Close()

	completionsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/edits" {
			if r.Header.Get("Authorization") != "Bearer phase5-edits-key" {
				t.Errorf("Edits provider request: %s %v", r.URL, r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"model":"phase5-edit"`) || !strings.Contains(string(body), `"instruction":"fix it"`) || !strings.Contains(string(body), `"unknown_extension":{"keep":true}`) {
				t.Errorf("Edits provider lost fields: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"edit","created":1,"choices":[{"text":"fixed"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
			return
		}
		if r.URL.Path == "/v1/completions" {
			if r.Header.Get("Authorization") != "Bearer phase5-completions-key" {
				t.Errorf("Completions provider request: %s %v", r.URL, r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"model":"phase5-gpt"`) || !strings.Contains(string(body), `phase5-system-instruction\nhi`) {
				t.Errorf("Completions system prompt was not prepended: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"cmpl_phase5","choices":[{"text":"fixed"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
			return
		}
		t.Errorf("unexpected provider path: %s", r.URL.Path)
	}))
	defer completionsServer.Close()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" || r.Header.Get("Authorization") != "phase5-proxy-key" || r.Header.Get("X-Trace") != "keep" {
			t.Errorf("Proxy upstream request: %s %v", r.URL, r.Header)
		}
		_, _ = io.WriteString(w, "proxy ok")
	}))
	defer proxyServer.Close()

	responsesURL, imagesURL := responsesServer.URL, imagesServer.URL
	speechURL, transcriptionURL := speechServer.URL, transcriptionServer.URL
	completionsURL, proxyURL := completionsServer.URL, proxyServer.URL
	responsesMapping, speechMapping := `{"responses-sdk":"responses-upstream"}`, `{"tts-1":"phase5-tts"}`
	editsMapping := `{"text-davinci-edit-001":"phase5-edit"}`
	completionsPrompt := "phase5-system-instruction"
	var responsesChannel *model.Channel
	for _, channel := range []*model.Channel{
		{Type: channeltype.OpenAI, Key: "phase5-responses-key", Name: "phase5-responses", Status: model.ChannelStatusEnabled, Group: "default", Models: "responses-sdk", ModelMapping: &responsesMapping, BaseURL: &responsesURL},
		{Type: channeltype.OpenAI, Key: "phase5-images-key", Name: "phase5-images", Status: model.ChannelStatusEnabled, Group: "default", Models: "dall-e-3", BaseURL: &imagesURL},
		{Type: channeltype.OpenAI, Key: "phase5-speech-key", Name: "phase5-speech", Status: model.ChannelStatusEnabled, Group: "default", Models: "tts-1", ModelMapping: &speechMapping, BaseURL: &speechURL},
		{Type: channeltype.OpenAI, Key: "phase5-whisper-key", Name: "phase5-whisper", Status: model.ChannelStatusEnabled, Group: "default", Models: "whisper-1", BaseURL: &transcriptionURL},
		{Type: channeltype.OpenAI, Key: "phase5-edits-key", Name: "phase5-edits", Status: model.ChannelStatusEnabled, Group: "default", Models: "text-davinci-edit-001", ModelMapping: &editsMapping, BaseURL: &completionsURL},
		{Type: channeltype.OpenAI, Key: "phase5-completions-key", Name: "phase5-completions", Status: model.ChannelStatusEnabled, Group: "default", Models: "phase5-gpt", SystemPrompt: &completionsPrompt, BaseURL: &completionsURL},
		{Type: channeltype.Proxy, Key: "phase5-proxy-key", Name: "phase5-proxy", Status: model.ChannelStatusEnabled, Group: "default", Models: "", BaseURL: &proxyURL},
	} {
		if err := channel.Insert(); err != nil {
			t.Fatal(err)
		}
		if channel.Name == "phase5-responses" {
			responsesChannel = channel
		}
	}

	gin.SetMode(gin.TestMode)
	routes := gin.New()
	SetRelayRouter(routes)
	gateway := httptest.NewServer(routes)
	defer gateway.Close()
	openaiClient := openai.NewClient(openaioption.WithBaseURL(gateway.URL+"/v1"), openaioption.WithAPIKey("sk-phase5-gateway-key"), openaioption.WithMaxRetries(0))
	ctx := context.Background()

	quotaAtStart, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}

	// Background lifecycle: create returns queued, retrieval returns the
	// completed upstream response under the gateway-assigned ID.
	created, err := openaiClient.Responses.New(ctx, responses.ResponseNewParams{
		Model: "responses-sdk", Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hello")},
		Background: openai.Bool(true),
	})
	if err != nil || created.Status != responses.ResponseStatusQueued || created.ID == "" {
		t.Fatalf("background create: status %q id %q err %v", created.Status, created.ID, err)
	}
	var completed *responses.Response
	deadline := time.Now().Add(5 * time.Second)
	for completed == nil {
		polled, pollErr := openaiClient.Responses.Get(ctx, created.ID, responses.ResponseGetParams{})
		if pollErr != nil {
			t.Fatalf("background retrieval: %v", pollErr)
		}
		if polled.Status != responses.ResponseStatusQueued && polled.Status != responses.ResponseStatusInProgress {
			completed = polled
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background response never completed: %s", polled.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if completed.ID != created.ID || completed.Status != responses.ResponseStatusCompleted {
		t.Fatalf("background completion: id %q status %q", completed.ID, completed.Status)
	}
	var completedOutput struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	rawCompleted, _ := json.Marshal(completed)
	if json.Unmarshal(rawCompleted, &completedOutput) != nil || len(completedOutput.Output) == 0 || completedOutput.Output[0].Content[0].Text != "bg ok" {
		t.Fatalf("background response output lost: %s", rawCompleted)
	}
	time.Sleep(100 * time.Millisecond)
	quotaAfterBackground, err := model.GetUserQuota(user.Id)
	var backgroundLog model.Log
	if err != nil || quotaAfterBackground >= quotaAtStart || db.Where("channel_id = ? AND type = ?", responsesChannel.Id, model.LogTypeConsume).Order("id desc").First(&backgroundLog).Error != nil {
		t.Fatalf("background response did not settle: before %d after %d err %v", quotaAtStart, quotaAfterBackground, err)
	}

	// Cancellation: a held background response is cancelled, stays cancelled,
	// and the reservation is refunded.
	cancelledCreate, err := openaiClient.Responses.New(ctx, responses.ResponseNewParams{
		Model: "responses-sdk", Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hold me")},
		Background: openai.Bool(true),
	}, openaioption.WithJSONSet("hold", true))
	if err != nil || cancelledCreate.Status != responses.ResponseStatusQueued {
		t.Fatalf("background cancel create: %+v, %v", cancelledCreate, err)
	}
	select {
	case <-responsesStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation fixture never received the upstream request")
	}
	cancelled, err := openaiClient.Responses.Cancel(ctx, cancelledCreate.ID)
	if err != nil || cancelled.Status != responses.ResponseStatusCancelled || cancelled.ID != cancelledCreate.ID {
		t.Fatalf("background cancel: %+v, %v", cancelled, err)
	}
	cancelAgain, err := openaiClient.Responses.Cancel(ctx, cancelledCreate.ID)
	if err != nil || cancelAgain.Status != responses.ResponseStatusCancelled {
		t.Fatalf("background cancel is not idempotent: %+v, %v", cancelAgain, err)
	}
	time.Sleep(100 * time.Millisecond)
	quotaAfterCancel, err := model.GetUserQuota(user.Id)
	if err != nil || quotaAfterCancel != quotaAfterBackground {
		t.Fatalf("cancelled background response was billed: before %d after %d err %v", quotaAfterBackground, quotaAfterCancel, err)
	}

	// Upstream failure surfaces through retrieval with a failed status.
	failedCreate, err := openaiClient.Responses.New(ctx, responses.ResponseNewParams{
		Model: "responses-sdk", Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("fail")},
		Background: openai.Bool(true),
	}, openaioption.WithJSONSet("fail", true))
	if err != nil || failedCreate.Status != responses.ResponseStatusQueued {
		t.Fatalf("background failure create: %+v, %v", failedCreate, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		polled, pollErr := openaiClient.Responses.Get(ctx, failedCreate.ID, responses.ResponseGetParams{})
		if pollErr != nil {
			t.Fatalf("failed background retrieval: %v", pollErr)
		}
		if polled.Status != responses.ResponseStatusQueued && polled.Status != responses.ResponseStatusInProgress {
			if polled.Status != responses.ResponseStatusFailed || !strings.Contains(polled.Error.Message, "phase5 busy") {
				t.Fatalf("failed background status or error wrong: %+v", polled)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed background response never settled")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Images bill the size and quality ratio times the image count.
	quotaBeforeImages, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	image, err := openaiClient.Images.Generate(ctx, openai.ImageGenerateParams{
		Model: "dall-e-3", Prompt: "a tree",
		Quality: openai.ImageGenerateParamsQualityHD, Size: openai.ImageGenerateParamsSize1024x1024,
	})
	if err != nil || len(image.Data) != 1 || image.Data[0].URL == "" {
		t.Fatalf("official image generation: %+v, %v", image, err)
	}
	time.Sleep(100 * time.Millisecond)
	quotaAfterImages, err := model.GetUserQuota(user.Id)
	var imageLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "dall-e-3", model.LogTypeConsume).Order("id desc").First(&imageLog).Error; err != nil {
		t.Fatalf("image log missing: %v", err)
	}
	expectedImageQuota := int64(billingratio.GetModelRatio("dall-e-3", channeltype.OpenAI) * billingratio.GetGroupRatio("default") * 2 * 1000)
	if err != nil || quotaAfterImages != quotaBeforeImages-int64(imageLog.Quota) || int64(imageLog.Quota) != expectedImageQuota || imageLog.PromptTokens != 0 {
		t.Fatalf("image quota wrong: expected %d logged %d before %d after %d prompt %d err %v", expectedImageQuota, imageLog.Quota, quotaBeforeImages, quotaAfterImages, imageLog.PromptTokens, err)
	}

	// Speech charges the input length through the mapped model ratio and
	// forwards the binary response.
	speech, err := openaiClient.Audio.Speech.New(ctx, openai.AudioSpeechNewParams{
		Model: "tts-1", Input: "hello audio",
		Voice: openai.AudioSpeechNewParamsVoiceUnion{OfAudioSpeechNewsVoiceString2: openai.String("alloy")},
	})
	if err != nil {
		t.Fatalf("official speech request: %v", err)
	}
	audioBody, _ := io.ReadAll(speech.Body)
	_ = speech.Body.Close()
	if string(audioBody) != "\x01\x02\x03binaryaudio" {
		t.Fatalf("speech binary response changed: %q", audioBody)
	}
	time.Sleep(100 * time.Millisecond)
	var speechLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "phase5-tts", model.LogTypeConsume).Order("id desc").First(&speechLog).Error; err != nil {
		t.Fatalf("speech log missing: %v", err)
	}
	expectedSpeechQuota := int64(float64(len("hello audio")) * billingratio.GetModelRatio("phase5-tts", channeltype.OpenAI) * billingratio.GetGroupRatio("default"))
	if int64(speechLog.Quota) != expectedSpeechQuota || int64(speechLog.PromptTokens) != expectedSpeechQuota {
		t.Fatalf("speech quota wrong: expected %d logged quota %d prompt %d", expectedSpeechQuota, speechLog.Quota, speechLog.PromptTokens)
	}

	// Transcriptions forward the multipart body untouched and bill the
	// transcript token count.
	transcription, err := openaiClient.Audio.Transcriptions.New(ctx, openai.AudioTranscriptionNewParams{
		Model: openai.AudioModel("whisper-1"),
		File:  openai.File(bytes.NewReader([]byte("RIFFwavdata")), "audio.wav", "audio/wav"),
	})
	if err != nil || transcription.Text != "hello transcript" {
		t.Fatalf("official transcription: %+v, %v", transcription, err)
	}
	time.Sleep(100 * time.Millisecond)
	var whisperLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "whisper-1", model.LogTypeConsume).Order("id desc").First(&whisperLog).Error; err != nil {
		t.Fatalf("transcription log missing: %v", err)
	}
	if whisperLog.Quota == 0 || whisperLog.PromptTokens != whisperLog.Quota {
		t.Fatalf("transcription quota wrong: quota %d prompt %d", whisperLog.Quota, whisperLog.PromptTokens)
	}

	// Edits forward unknown extensions and settle on provider usage.
	editRequest := httptest.NewRequest(http.MethodPost, "/v1/edits", strings.NewReader(`{"model":"text-davinci-edit-001","input":"broken","instruction":"fix it","unknown_extension":{"keep":true}}`))
	editRequest.Header.Set("Content-Type", "application/json")
	editRequest.Header.Set("Authorization", "Bearer sk-phase5-gateway-key")
	editResponse := httptest.NewRecorder()
	routes.ServeHTTP(editResponse, editRequest)
	if editResponse.Code != http.StatusOK || !strings.Contains(editResponse.Body.String(), `"text":"fixed"`) {
		t.Fatalf("edits passthrough changed: status %d body %s", editResponse.Code, editResponse.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	var editLog model.Log
	if err := db.Where("model_name = ? AND type = ?", "phase5-edit", model.LogTypeConsume).Order("id desc").First(&editLog).Error; err != nil || editLog.PromptTokens != 4 || editLog.CompletionTokens != 2 {
		t.Fatalf("edits usage was not settled: %+v err %v", editLog, err)
	}

	// A configured system prompt is prepended to Completions prompts.
	completion, err := openaiClient.Completions.New(ctx, openai.CompletionNewParams{
		Model: openai.CompletionNewParamsModel("phase5-gpt"),
		Prompt: openai.CompletionNewParamsPromptUnion{
			OfString: openai.String("hi"),
		},
	})
	if err != nil || len(completion.Choices) != 1 || completion.Choices[0].Text != "fixed" {
		t.Fatalf("completions through the shared lifecycle: %+v, %v", completion, err)
	}

	// The proxy route stays unmetered and substitutes the channel key.
	proxyChannel := model.Channel{}
	if err := db.Where("name = ?", "phase5-proxy").First(&proxyChannel).Error; err != nil {
		t.Fatal(err)
	}
	quotaBeforeProxy, err := model.GetUserQuota(user.Id)
	if err != nil {
		t.Fatal(err)
	}
	proxyRequest := httptest.NewRequest(http.MethodGet, "/v1/oneapi/proxy/"+strconv.Itoa(proxyChannel.Id)+"/health", nil)
	proxyRequest.Header.Set("X-Trace", "keep")
	proxyRequest.Header.Set("Authorization", "Bearer sk-phase5-gateway-key")
	proxyResponse := httptest.NewRecorder()
	routes.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK || proxyResponse.Body.String() != "proxy ok" {
		t.Fatalf("proxy route changed: status %d body %s", proxyResponse.Code, proxyResponse.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	quotaAfterProxy, err := model.GetUserQuota(user.Id)
	var proxyLogs int64
	if err := db.Model(&model.Log{}).Where("channel_id = ?", proxyChannel.Id).Count(&proxyLogs).Error; err != nil {
		t.Fatal(err)
	}
	if err != nil || quotaAfterProxy != quotaBeforeProxy || proxyLogs != 0 {
		t.Fatalf("proxy route billed or misrouted: before %d after %d logs %d err %v", quotaBeforeProxy, quotaAfterProxy, proxyLogs, err)
	}
}

// Provider channels keep their adapter-backed embeddings while operations the
// adapters never supported fail before reaching the upstream.
func TestPhase5ProviderChannelEmbeddingsFallbackAndRejections(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := commonclient.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:phase5-provider?mode=memory&cache=shared"), &gorm.Config{})
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
	commonclient.HTTPClient = &http.Client{}
	common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite = false, false, true
	config.RetryTimes = 0
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		commonclient.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes = previousRedis, previousMemoryCache, previousRetries
		common.UsingSQLite = previousSQLite
		_ = sqlDB.Close()
	})
	user := model.User{Username: "phase5-provider", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "phase5-provider-key", Name: "phase5-provider", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	var upstreamCalls atomic.Int64
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"upstream-ollama-embed"`) || !strings.Contains(string(body), `"input"`) {
			t.Errorf("Ollama embeddings request lost fields: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"upstream-ollama-embed","embeddings":[[0.1,0.2]]}`)
	}))
	defer ollamaServer.Close()

	ollamaURL := ollamaServer.URL
	ollamaMapping := `{"ollama-embed":"upstream-ollama-embed"}`
	ollamaChannel := model.Channel{Type: channeltype.Ollama, Key: "phase5-ollama-key", Name: "phase5-ollama", Status: model.ChannelStatusEnabled, Group: "default", Models: "ollama-embed", ModelMapping: &ollamaMapping, BaseURL: &ollamaURL}
	if err := ollamaChannel.Insert(); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	routes := gin.New()
	SetRelayRouter(routes)
	gateway := httptest.NewServer(routes)
	defer gateway.Close()

	embeddingsRequest := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"ollama-embed","input":["hello"]}`))
	embeddingsRequest.Header.Set("Content-Type", "application/json")
	embeddingsRequest.Header.Set("Authorization", "Bearer sk-phase5-provider-key")
	embeddingsResponse := httptest.NewRecorder()
	routes.ServeHTTP(embeddingsResponse, embeddingsRequest)
	if embeddingsResponse.Code != http.StatusOK || !strings.Contains(embeddingsResponse.Body.String(), `"embedding":[0.1,0.2]`) {
		t.Fatalf("provider embeddings fallback changed: status %d body %s", embeddingsResponse.Code, embeddingsResponse.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	var embeddingsLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", ollamaChannel.Id, model.LogTypeConsume).First(&embeddingsLog).Error; err != nil {
		t.Fatalf("provider embeddings were not settled: %v", err)
	}

	for _, fixture := range []struct{ path, body string }{
		{"/v1/edits", `{"model":"ollama-embed","instruction":"fix"}`},
		{"/v1/moderations", `{"model":"ollama-embed","input":"text"}`},
		{"/v1/audio/speech", `{"model":"ollama-embed","input":"text","voice":"alloy"}`},
	} {
		rejected := httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body))
		rejected.Header.Set("Content-Type", "application/json")
		rejected.Header.Set("Authorization", "Bearer sk-phase5-provider-key")
		rejectedResponse := httptest.NewRecorder()
		routes.ServeHTTP(rejectedResponse, rejected)
		if rejectedResponse.Code != http.StatusUnprocessableEntity || upstreamCalls.Load() != 1 {
			t.Fatalf("unsupported operation reached the provider: %s status %d calls %d body %s", fixture.path, rejectedResponse.Code, upstreamCalls.Load(), rejectedResponse.Body.String())
		}
	}
}

// The provider image formats run through the shared lifecycle with image-size
// billing: the zhipu adapter converts the mapped request, the upstream image
// response is forwarded untouched, and the fixed quota is settled exactly.
func TestPhase5ProviderImageRouteUsesLifecycleBilling(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousClient := commonclient.HTTPClient
	previousRedis, previousMemoryCache, previousRetries := common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes
	previousSQLite := common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:phase5-image?mode=memory&cache=shared"), &gorm.Config{})
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
	commonclient.HTTPClient = &http.Client{}
	common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite = false, false, true
	config.RetryTimes = 0
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		commonclient.HTTPClient = previousClient
		common.RedisEnabled, config.MemoryCacheEnabled, config.RetryTimes = previousRedis, previousMemoryCache, previousRetries
		common.UsingSQLite = previousSQLite
		_ = sqlDB.Close()
	})
	user := model.User{Username: "phase5-image", Status: model.UserStatusEnabled, Role: model.RoleAdminUser, Group: "default", Quota: 1000000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "phase5-image-key", Name: "phase5-image", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	zhipuServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/paas/v4/images/generations" {
			t.Errorf("zhipu image provider request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"upstream-cogview"`) || !strings.Contains(string(body), `"prompt":"a lake"`) {
			t.Errorf("zhipu image request lost fields: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":7,"data":[{"url":"https://example.test/lake.png"}]}`)
	}))
	defer zhipuServer.Close()

	zhipuURL := zhipuServer.URL
	zhipuMapping := `{"cogview-3":"upstream-cogview"}`
	zhipuChannel := model.Channel{Type: channeltype.Zhipu, Key: "phase5-zhipu-key", Name: "phase5-zhipu", Status: model.ChannelStatusEnabled, Group: "default", Models: "cogview-3", ModelMapping: &zhipuMapping, BaseURL: &zhipuURL}
	if err := zhipuChannel.Insert(); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	routes := gin.New()
	SetRelayRouter(routes)
	gateway := httptest.NewServer(routes)
	defer gateway.Close()

	imageRequest := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"cogview-3","prompt":"a lake"}`))
	imageRequest.Header.Set("Content-Type", "application/json")
	imageRequest.Header.Set("Authorization", "Bearer sk-phase5-image-key")
	imageResponse := httptest.NewRecorder()
	routes.ServeHTTP(imageResponse, imageRequest)
	if imageResponse.Code != http.StatusOK || !strings.Contains(imageResponse.Body.String(), "https://example.test/lake.png") {
		t.Fatalf("provider image route changed: status %d body %s", imageResponse.Code, imageResponse.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	var imageLog model.Log
	if err := db.Where("channel_id = ? AND type = ?", zhipuChannel.Id, model.LogTypeConsume).First(&imageLog).Error; err != nil {
		t.Fatalf("provider image quota was not settled: %v", err)
	}
	expectedQuota := int64(billingratio.GetModelRatio("upstream-cogview", channeltype.Zhipu) * billingratio.GetGroupRatio("default") * 1000)
	if int64(imageLog.Quota) != expectedQuota {
		t.Fatalf("provider image quota %d does not match the size-billing estimate %d", imageLog.Quota, expectedQuota)
	}
	if imageLog.PromptTokens != 0 {
		t.Fatalf("provider image consume log should not report prompt tokens: %d", imageLog.PromptTokens)
	}
}
