package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Protocol string

const (
	Anthropic Protocol = "anthropic"
	Gemini    Protocol = "gemini"
)

type Request struct {
	Protocol Protocol
	BaseURL  string
	Version  string
	Model    string
	Action   string
	APIKey   string
	Body     []byte
	Headers  http.Header
	Query    url.Values
}

// BuildRequest changes only the fields required to reach the selected upstream.
// In particular, it never forwards the client's gateway token to the provider.
func BuildRequest(ctx context.Context, input Request) (*http.Request, error) {
	base, err := url.Parse(input.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Fragment != "" || base.RawQuery != "" {
		return nil, errors.New("invalid upstream base URL")
	}
	if input.APIKey == "" {
		return nil, errors.New("upstream API key is empty")
	}
	var path string
	body := input.Body
	stream := false
	switch input.Protocol {
	case Anthropic:
		path = "/v1/messages"
		body, stream, err = prepareAnthropicBody(body, input.Model)
		if err != nil {
			return nil, err
		}
	case Gemini:
		if input.Model == "" || strings.ContainsAny(input.Model, "/?#:") {
			return nil, errors.New("invalid Gemini model name")
		}
		if input.Action != "generateContent" && input.Action != "streamGenerateContent" {
			return nil, errors.New("unsupported Gemini action")
		}
		version := input.Version
		if version == "" {
			version = "v1beta"
		}
		if version != "v1" && version != "v1beta" {
			return nil, errors.New("unsupported Gemini API version")
		}
		path = fmt.Sprintf("/%s/models/%s:%s", version, url.PathEscape(input.Model), input.Action)
		stream = input.Action == "streamGenerateContent"
	default:
		return nil, errors.New("unsupported native protocol")
	}
	endpoint := strings.TrimRight(base.String(), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	query := make(url.Values)
	for name, values := range input.Query {
		if strings.EqualFold(name, "key") || strings.EqualFold(name, "api_key") {
			continue
		}
		query[name] = append([]string(nil), values...)
	}
	if stream && input.Protocol == Gemini {
		query.Set("alt", "sse")
	}
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	switch input.Protocol {
	case Anthropic:
		req.Header.Set("x-api-key", input.APIKey)
		version := input.Headers.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
		for _, name := range []string{"anthropic-beta", "anthropic-workspace-id"} {
			if value := input.Headers.Get(name); value != "" {
				req.Header.Set(name, value)
			}
		}
	case Gemini:
		req.Header.Set("x-goog-api-key", input.APIKey)
	}
	return req, nil
}

func prepareAnthropicBody(body []byte, model string) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false, errors.New("invalid Anthropic JSON request")
	}
	var requestedModel string
	if err := json.Unmarshal(fields["model"], &requestedModel); err != nil || requestedModel == "" {
		return nil, false, errors.New("Anthropic model is required")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, false, errors.New("Anthropic stream must be boolean")
		}
	}
	if model == "" || model == requestedModel {
		return body, stream, nil
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, false, err
	}
	fields["model"] = encoded
	body, err = json.Marshal(fields)
	return body, stream, err
}
