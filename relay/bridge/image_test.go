package bridge

import (
	"encoding/json"
	"testing"
)

func TestChatAndMessagesImageConversion(t *testing.T) {
	chat := []byte(`{"model":"alias","messages":[{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}},{"type":"image_url","image_url":{"url":"https://example.com/photo.webp"}}]}]}`)
	toMessages, err := ChatToAnthropic(chat, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var messages struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
					URL       string `json:"url"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(toMessages, &messages); err != nil {
		t.Fatal(err)
	}
	parts := messages.Messages[0].Content
	if len(parts) != 3 || parts[0].Type != "text" || parts[1].Source.MediaType != "image/png" || parts[1].Source.Data != "aGVsbG8=" || parts[2].Source.URL != "https://example.com/photo.webp" {
		t.Fatalf("Chat images were changed: %s", toMessages)
	}

	fromMessages, err := AnthropicToChatRequest([]byte(`{"model":"alias","max_tokens":32,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},{"type":"image","source":{"type":"url","url":"https://example.com/photo.webp"}}]}]}`), "gpt")
	if err != nil {
		t.Fatal(err)
	}
	var converted struct {
		Messages []struct {
			Content []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(fromMessages, &converted); err != nil {
		t.Fatal(err)
	}
	images := converted.Messages[0].Content
	if len(images) != 2 || images[0].ImageURL.URL != "data:image/png;base64,aGVsbG8=" || images[1].ImageURL.URL != "https://example.com/photo.webp" {
		t.Fatalf("Messages images were changed: %s", fromMessages)
	}
}

func TestImageConversionRejectsUnrepresentableInput(t *testing.T) {
	for _, part := range []string{
		`{"type":"image_url","image_url":{"url":"data:image/svg+xml;base64,aGVsbG8="}}`,
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,!!!"}}`,
		`{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"low"}}`,
		`{"type":"image_url","image_url":{"url":"file:///etc/passwd"}}`,
	} {
		body := `{"model":"alias","messages":[{"role":"user","content":[` + part + `]}]}`
		if _, err := ChatToAnthropic([]byte(body), "claude"); err == nil {
			t.Fatalf("unsupported Chat image was accepted: %s", part)
		}
	}
	for _, source := range []string{
		`{"type":"base64","media_type":"image/png","data":"!!!"}`,
		`{"type":"base64","media_type":"image/svg+xml","data":"aGVsbG8="}`,
		`{"type":"url","url":"file:///etc/passwd"}`,
	} {
		body := `{"model":"alias","max_tokens":32,"messages":[{"role":"user","content":[{"type":"image","source":` + source + `}]}]}`
		if _, err := AnthropicToChatRequest([]byte(body), "gpt"); err == nil {
			t.Fatalf("unsupported Messages image was accepted: %s", source)
		}
	}
	if _, err := ChatToAnthropic([]byte(`{"model":"alias","messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`), "claude"); err == nil {
		t.Fatal("assistant image was accepted")
	}
	if _, err := AnthropicToChatRequest([]byte(`{"model":"alias","max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`), "gpt"); err == nil {
		t.Fatal("assistant image was accepted")
	}
}
