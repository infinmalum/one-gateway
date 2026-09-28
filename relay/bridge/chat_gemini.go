package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/infinmalum/one-gateway/relay/native"
)

// ChatToGemini converts text conversations. Tool calls, media, and
// provider-specific Chat fields need separate converters and are rejected
// before the upstream call. Streaming requests convert their body; the
// upstream stream action is selected by the transport.
func ChatToGemini(body []byte, model string) ([]byte, error) {
	if model == "" {
		return nil, errors.New("model is required")
	}
	fields, err := object(body, "model", "messages", "stream", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop")
	if err != nil {
		return nil, err
	}
	var stream bool
	if err := decode(fields["stream"], &stream); err != nil {
		return nil, errors.New("stream must be boolean")
	}
	var messages []json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	var maxTokens, maxCompletion int64
	if err := decode(fields["max_tokens"], &maxTokens); err != nil || maxTokens < 0 {
		return nil, errors.New("max_tokens must not be negative")
	}
	if err := decode(fields["max_completion_tokens"], &maxCompletion); err != nil || maxCompletion < 0 {
		return nil, errors.New("max_completion_tokens must not be negative")
	}
	if maxTokens > 0 && maxCompletion > 0 && maxTokens != maxCompletion {
		return nil, errors.New("conflicting Chat token limits")
	}
	config := map[string]any{}
	if limit := max(maxTokens, maxCompletion); limit > 0 {
		config["maxOutputTokens"] = limit
	}
	for _, pair := range [][2]string{{"temperature", "temperature"}, {"top_p", "topP"}} {
		if len(fields[pair[0]]) != 0 {
			var value float64
			if json.Unmarshal(fields[pair[0]], &value) != nil {
				return nil, fmt.Errorf("%s must be a number", pair[0])
			}
			config[pair[1]] = value
		}
	}
	if len(fields["stop"]) != 0 {
		var single string
		var stops []string
		if json.Unmarshal(fields["stop"], &single) == nil {
			stops = []string{single}
		} else if json.Unmarshal(fields["stop"], &stops) != nil || len(stops) > 5 {
			return nil, errors.New("stop must contain at most five strings")
		}
		config["stopSequences"] = stops
	}
	request := map[string]any{}
	var contents []any
	var system []string
	for _, raw := range messages {
		message, err := object(raw, "role", "content")
		if err != nil {
			return nil, err
		}
		var role string
		_ = json.Unmarshal(message["role"], &role)
		parts, err := geminiTextParts(message["content"])
		if err != nil {
			return nil, err
		}
		switch role {
		case "system":
			if len(contents) != 0 {
				return nil, errors.New("system messages must precede conversation messages")
			}
			for _, part := range parts {
				system = append(system, part["text"])
			}
		case "user", "assistant":
			geminiRole := "user"
			if role == "assistant" {
				geminiRole = "model"
			}
			contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
		default:
			return nil, fmt.Errorf("Chat role %q cannot be converted to Gemini", role)
		}
	}
	if len(contents) == 0 {
		return nil, errors.New("conversation messages are required")
	}
	request["contents"] = contents
	if len(system) > 0 {
		request["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": joinSystem(system)}}}
	}
	if len(config) > 0 {
		request["generationConfig"] = config
	}
	return json.Marshal(request)
}

func geminiTextParts(raw json.RawMessage) ([]map[string]string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []map[string]string{{"text": text}}, nil
	}
	var rawParts []json.RawMessage
	if json.Unmarshal(raw, &rawParts) != nil || len(rawParts) == 0 {
		return nil, errors.New("Gemini conversion requires text content")
	}
	var parts []map[string]string
	for _, rawPart := range rawParts {
		part, err := object(rawPart, "type", "text")
		if err != nil {
			return nil, err
		}
		var kind, value string
		_ = json.Unmarshal(part["type"], &kind)
		if kind != "text" || len(part) != 2 || json.Unmarshal(part["text"], &value) != nil {
			return nil, errors.New("Gemini conversion supports only Chat text parts")
		}
		parts = append(parts, map[string]string{"text": value})
	}
	return parts, nil
}

// GeminiToChat keeps provider metadata in a named response extension while
// rejecting response content and finish states Chat cannot represent.
func GeminiToChat(body []byte, requestedModel string) ([]byte, native.Usage, error) {
	fields, err := object(body, "candidates", "promptFeedback", "usageMetadata", "modelVersion", "responseId", "modelStatus")
	if err != nil {
		return nil, native.Usage{}, err
	}
	var id string
	_ = json.Unmarshal(fields["responseId"], &id)
	if id == "" {
		var randomID [12]byte
		if _, err := rand.Read(randomID[:]); err != nil {
			return nil, native.Usage{}, errors.New("failed to create Chat response ID")
		}
		id = "chatcmpl_" + hex.EncodeToString(randomID[:])
	}
	var rawCandidates []json.RawMessage
	if json.Unmarshal(fields["candidates"], &rawCandidates) != nil || len(rawCandidates) != 1 {
		return nil, native.Usage{}, errors.New("Gemini response requires one candidate")
	}
	candidate, err := object(rawCandidates[0], "content", "finishReason", "safetyRatings", "citationMetadata", "tokenCount", "groundingAttributions", "groundingMetadata", "avgLogprobs", "logprobsResult", "urlContextMetadata", "index", "finishMessage")
	if err != nil {
		return nil, native.Usage{}, err
	}
	if len(candidate["index"]) != 0 {
		var index int
		if json.Unmarshal(candidate["index"], &index) != nil || index != 0 {
			return nil, native.Usage{}, errors.New("Gemini candidate index must be zero")
		}
	}
	var finish string
	_ = json.Unmarshal(candidate["finishReason"], &finish)
	chatFinish := ""
	switch finish {
	case "STOP":
		chatFinish = "stop"
	case "MAX_TOKENS":
		chatFinish = "length"
	default:
		return nil, native.Usage{}, fmt.Errorf("Gemini finish reason %q cannot be converted", finish)
	}
	content, err := object(candidate["content"], "role", "parts")
	if err != nil {
		return nil, native.Usage{}, err
	}
	var role string
	_ = json.Unmarshal(content["role"], &role)
	if role != "" && role != "model" {
		return nil, native.Usage{}, errors.New("Gemini response content role must be model")
	}
	var rawParts []json.RawMessage
	if json.Unmarshal(content["parts"], &rawParts) != nil || len(rawParts) == 0 {
		return nil, native.Usage{}, errors.New("Gemini response requires text parts")
	}
	var text string
	for _, raw := range rawParts {
		part, err := object(raw, "text")
		if err != nil || len(part) != 1 {
			return nil, native.Usage{}, errors.New("Gemini response contains a part Chat cannot represent")
		}
		var value string
		if json.Unmarshal(part["text"], &value) != nil {
			return nil, native.Usage{}, errors.New("Gemini text part must contain text")
		}
		text += value
	}
	prompt, output, cached, err := geminiUsage(fields["usageMetadata"], true)
	if err != nil {
		return nil, native.Usage{}, err
	}
	usage := native.Usage{Input: prompt, Output: output, Seen: true, Complete: true}
	result := map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": requestedModel,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": text}, "finish_reason": chatFinish}},
		"usage":   map[string]any{"prompt_tokens": usage.Input, "completion_tokens": usage.Output, "total_tokens": usage.Input + usage.Output},
	}
	if cached > 0 {
		result["usage"].(map[string]any)["prompt_tokens_details"] = map[string]int64{"cached_tokens": cached}
	}
	metadata := map[string]json.RawMessage{"usageMetadata": fields["usageMetadata"]}
	for _, name := range []string{"modelVersion", "modelStatus", "promptFeedback"} {
		if len(fields[name]) != 0 {
			metadata[name] = fields[name]
		}
	}
	for name, value := range candidate {
		if name != "content" && name != "finishReason" {
			metadata[name] = value
		}
	}
	result["provider_metadata"] = map[string]any{"gemini": metadata}
	converted, err := json.Marshal(result)
	return converted, usage, err
}
