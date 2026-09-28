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
	Anthropic         Protocol = "anthropic"
	Gemini            Protocol = "gemini"
	OpenAIChat        Protocol = "openai_chat"
	OpenAICompletions Protocol = "openai_completions"
	OpenAIEmbeddings  Protocol = "openai_embeddings"
	OpenAIModerations Protocol = "openai_moderations"
	OpenAIResponses   Protocol = "openai_responses"
)

type Request struct {
	Protocol     Protocol
	BaseURL      string
	Version      string
	Model        string
	Action       string
	APIKey       string
	SystemPrompt string
	Body         []byte
	Headers      http.Header
	Query        url.Values
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
		body, stream, err = prepareAnthropicBody(body, input.Model, input.SystemPrompt)
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
		if input.SystemPrompt != "" {
			body, err = prepareGeminiBody(body, input.SystemPrompt)
			if err != nil {
				return nil, err
			}
		}
	case OpenAIResponses:
		path = "/v1/responses"
		body, stream, err = prepareOpenAIResponsesBody(body, input.Model, input.SystemPrompt)
		if err != nil {
			return nil, err
		}
	case OpenAIChat:
		path = "/v1/chat/completions"
		body, stream, err = prepareOpenAIChatBody(body, input.Model, input.SystemPrompt)
		if err != nil {
			return nil, err
		}
	case OpenAIEmbeddings:
		path = "/v1/embeddings"
		body, err = prepareOpenAIEmbeddingsBody(body, input.Model)
		if err != nil {
			return nil, err
		}
	case OpenAIModerations:
		path = "/v1/moderations"
		body, err = prepareOpenAIModerationsBody(body, input.Model)
		if err != nil {
			return nil, err
		}
	case OpenAICompletions:
		path = "/v1/completions"
		body, stream, err = prepareOpenAICompletionsBody(body, input.Model)
		if err != nil {
			return nil, err
		}
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
	case OpenAIResponses, OpenAIChat, OpenAICompletions, OpenAIEmbeddings, OpenAIModerations:
		req.Header.Set("Authorization", "Bearer "+input.APIKey)
		if beta := input.Headers.Get("OpenAI-Beta"); beta != "" {
			req.Header.Set("OpenAI-Beta", beta)
		}
	}
	return req, nil
}

func prepareOpenAIModerationsBody(body []byte, model string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, errors.New("invalid OpenAI Moderations JSON request")
	}
	if len(fields["input"]) == 0 || string(fields["input"]) == "null" {
		return nil, errors.New("OpenAI Moderations input is required")
	}
	var requestedModel string
	if raw, ok := fields["model"]; ok {
		if err := json.Unmarshal(raw, &requestedModel); err != nil {
			return nil, errors.New("OpenAI Moderations model must be a string")
		}
	}
	if model == "" || model == requestedModel {
		return body, nil
	}
	fields["model"], _ = json.Marshal(model)
	return json.Marshal(fields)
}

func prepareOpenAICompletionsBody(body []byte, model string) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false, errors.New("invalid OpenAI Completions JSON request")
	}
	var requestedModel string
	if err := json.Unmarshal(fields["model"], &requestedModel); err != nil || requestedModel == "" {
		return nil, false, errors.New("OpenAI Completions model is required")
	}
	if len(fields["prompt"]) == 0 || string(fields["prompt"]) == "null" {
		return nil, false, errors.New("OpenAI Completions prompt is required")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		return nil, false, errors.New("OpenAI Completions stream must be boolean")
	}
	if model == "" || model == requestedModel {
		return body, stream, nil
	}
	fields["model"], _ = json.Marshal(model)
	updated, err := json.Marshal(fields)
	return updated, stream, err
}

func prepareOpenAIEmbeddingsBody(body []byte, model string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, errors.New("invalid OpenAI Embeddings JSON request")
	}
	var requestedModel string
	if err := json.Unmarshal(fields["model"], &requestedModel); err != nil || requestedModel == "" {
		return nil, errors.New("OpenAI Embeddings model is required")
	}
	if model == "" || model == requestedModel {
		return body, nil
	}
	fields["model"], _ = json.Marshal(model)
	return json.Marshal(fields)
}

func prepareOpenAIChatBody(body []byte, model, systemPrompt string) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false, errors.New("invalid OpenAI Chat JSON request")
	}
	var requestedModel string
	if err := json.Unmarshal(fields["model"], &requestedModel); err != nil || requestedModel == "" {
		return nil, false, errors.New("OpenAI Chat model is required")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, false, errors.New("OpenAI Chat stream must be boolean")
		}
	}
	if (model == "" || model == requestedModel) && systemPrompt == "" {
		return body, stream, nil
	}
	if model != "" && model != requestedModel {
		fields["model"], _ = json.Marshal(model)
	}
	if systemPrompt != "" {
		var messages []json.RawMessage
		if err := json.Unmarshal(fields["messages"], &messages); err != nil || messages == nil {
			return nil, false, errors.New("OpenAI Chat messages must be an array")
		}
		instruction, _ := json.Marshal(map[string]string{"role": "system", "content": systemPrompt})
		if len(messages) > 0 {
			var first map[string]json.RawMessage
			if json.Unmarshal(messages[0], &first) == nil {
				var role string
				if json.Unmarshal(first["role"], &role) == nil && role == "system" {
					first["content"], _ = json.Marshal(systemPrompt)
					messages[0], _ = json.Marshal(first)
				} else {
					messages = append([]json.RawMessage{instruction}, messages...)
				}
			} else {
				messages = append([]json.RawMessage{instruction}, messages...)
			}
		} else {
			messages = append(messages, instruction)
		}
		fields["messages"], _ = json.Marshal(messages)
	}
	updated, err := json.Marshal(fields)
	return updated, stream, err
}

func prepareAnthropicBody(body []byte, model, systemPrompt string) ([]byte, bool, error) {
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
	if (model == "" || model == requestedModel) && systemPrompt == "" {
		return body, stream, nil
	}
	if model != "" && model != requestedModel {
		encoded, err := json.Marshal(model)
		if err != nil {
			return nil, false, err
		}
		fields["model"] = encoded
	}
	if systemPrompt != "" {
		encoded, err := json.Marshal(systemPrompt)
		if err != nil {
			return nil, false, err
		}
		fields["system"] = encoded
	}
	body, err := json.Marshal(fields)
	return body, stream, err
}

func prepareGeminiBody(body []byte, systemPrompt string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, errors.New("invalid Gemini JSON request")
	}
	instruction, err := json.Marshal(map[string]any{"parts": []map[string]string{{"text": systemPrompt}}})
	if err != nil {
		return nil, err
	}
	fields["systemInstruction"] = instruction
	return json.Marshal(fields)
}

func prepareOpenAIResponsesBody(body []byte, model, instructions string) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false, errors.New("invalid OpenAI Responses JSON request")
	}
	var requestedModel string
	if err := json.Unmarshal(fields["model"], &requestedModel); err != nil || requestedModel == "" {
		return nil, false, errors.New("OpenAI Responses model is required")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, false, errors.New("OpenAI Responses stream must be boolean")
		}
	}
	if (model == "" || model == requestedModel) && instructions == "" {
		return body, stream, nil
	}
	if model != "" && model != requestedModel {
		encoded, err := json.Marshal(model)
		if err != nil {
			return nil, false, err
		}
		fields["model"] = encoded
	}
	if instructions != "" {
		encoded, err := json.Marshal(instructions)
		if err != nil {
			return nil, false, err
		}
		fields["instructions"] = encoded
	}
	updated, err := json.Marshal(fields)
	return updated, stream, err
}
