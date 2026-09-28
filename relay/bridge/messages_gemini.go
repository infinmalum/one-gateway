package bridge

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/infinmalum/one-gateway/relay/native"
)

// MessagesToGemini converts the text Messages subset that Gemini
// GenerateContent can represent. Tools, media, and thinking need separate
// converters and are rejected before the upstream call. Streaming requests
// convert their body; the upstream stream action is selected by the transport.
func MessagesToGemini(body []byte, model string) ([]byte, error) {
	if model == "" {
		return nil, errors.New("model is required")
	}
	fields, err := object(body, "model", "messages", "max_tokens", "system", "stream", "temperature", "top_p", "top_k", "stop_sequences")
	if err != nil {
		return nil, err
	}
	var stream bool
	if err := decode(fields["stream"], &stream); err != nil {
		return nil, errors.New("stream must be boolean")
	}
	var limit int64
	if err := json.Unmarshal(fields["max_tokens"], &limit); err != nil || limit <= 0 {
		return nil, errors.New("positive max_tokens is required")
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(fields["messages"], &messages); err != nil || len(messages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	config := map[string]any{"maxOutputTokens": limit}
	for _, pair := range [][2]string{{"temperature", "temperature"}, {"top_p", "topP"}, {"top_k", "topK"}} {
		if len(fields[pair[0]]) != 0 {
			var value float64
			if json.Unmarshal(fields[pair[0]], &value) != nil {
				return nil, fmt.Errorf("%s must be a number", pair[0])
			}
			config[pair[1]] = value
		}
	}
	if len(fields["stop_sequences"]) != 0 {
		var stops []string
		if json.Unmarshal(fields["stop_sequences"], &stops) != nil || len(stops) > 5 {
			return nil, errors.New("stop_sequences must contain at most five strings")
		}
		config["stopSequences"] = stops
	}
	request := map[string]any{}
	if len(fields["system"]) != 0 {
		text, err := messagesText(fields["system"])
		if err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
		if text != "" {
			request["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": text}}}
		}
	}
	var contents []any
	for _, raw := range messages {
		message, err := object(raw, "role", "content")
		if err != nil {
			return nil, err
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil || (role != "user" && role != "assistant") {
			return nil, errors.New("Messages role must be user or assistant")
		}
		text, err := messagesText(message["content"])
		if err != nil || text == "" {
			return nil, errors.New("Gemini conversion requires text message content")
		}
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		contents = append(contents, map[string]any{"role": geminiRole, "parts": []map[string]string{{"text": text}}})
	}
	request["contents"] = contents
	request["generationConfig"] = config
	return json.Marshal(request)
}

// GeminiToMessages converts one Gemini candidate into an Anthropic message.
// Provider metadata is kept in a named extension while response content and
// finish states Messages cannot represent are rejected.
func GeminiToMessages(body []byte, requestedModel string) ([]byte, native.Usage, error) {
	fields, err := object(body, "candidates", "promptFeedback", "usageMetadata", "modelVersion", "responseId", "modelStatus")
	if err != nil {
		return nil, native.Usage{}, err
	}
	id := rawString(fields["responseId"])
	if id == "" {
		generated, err := newStreamID("msg_")
		if err != nil {
			return nil, native.Usage{}, err
		}
		id = generated
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
	finish := rawString(candidate["finishReason"])
	stop := ""
	switch finish {
	case "STOP":
		stop = "end_turn"
	case "MAX_TOKENS":
		stop = "max_tokens"
	default:
		return nil, native.Usage{}, fmt.Errorf("Gemini finish reason %q cannot be converted", finish)
	}
	content, err := object(candidate["content"], "role", "parts")
	if err != nil {
		return nil, native.Usage{}, err
	}
	if role := rawString(content["role"]); role != "" && role != "model" {
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
			return nil, native.Usage{}, errors.New("Gemini response contains a part Messages cannot represent")
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
	result := map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": requestedModel,
		"content":     []any{map[string]string{"type": "text", "text": text}},
		"stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]int64{"input_tokens": prompt - cached, "cache_creation_input_tokens": 0, "cache_read_input_tokens": cached, "output_tokens": output},
	}
	if len(metadata) > 0 {
		result["provider_metadata"] = map[string]any{"gemini": metadata}
	}
	converted, err := json.Marshal(result)
	return converted, usage, err
}

// geminiUsage validates the usageMetadata object shared by the sync and
// streaming Gemini converters and returns prompt, candidate, and cached token
// counts. Output includes thinking tokens because Messages and Chat bill them
// as completion output. Sync responses require an output count; streaming
// events may report only what the provider has produced so far.
func geminiUsage(raw json.RawMessage, requireOutput bool) (int64, int64, int64, error) {
	if len(raw) == 0 {
		return 0, 0, 0, errors.New("Gemini response usage is required")
	}
	usageRaw, err := object(raw, "promptTokenCount", "cachedContentTokenCount", "candidatesTokenCount", "toolUsePromptTokenCount", "thoughtsTokenCount", "totalTokenCount", "promptTokensDetails", "cacheTokensDetails", "candidatesTokensDetails", "toolUsePromptTokensDetails", "thoughtsTokensDetails", "trafficType")
	if err != nil || len(usageRaw["promptTokenCount"]) == 0 || (requireOutput && len(usageRaw["candidatesTokenCount"]) == 0 && len(usageRaw["totalTokenCount"]) == 0) {
		return 0, 0, 0, errors.New("Gemini response token counts are required")
	}
	var usageFields struct {
		Prompt     int64 `json:"promptTokenCount"`
		Candidates int64 `json:"candidatesTokenCount"`
		Thoughts   int64 `json:"thoughtsTokenCount"`
		Total      int64 `json:"totalTokenCount"`
		Cached     int64 `json:"cachedContentTokenCount"`
	}
	if json.Unmarshal(raw, &usageFields) != nil || usageFields.Prompt < 0 || usageFields.Candidates < 0 || usageFields.Thoughts < 0 || usageFields.Total < 0 || usageFields.Cached < 0 || usageFields.Cached > usageFields.Prompt {
		return 0, 0, 0, errors.New("invalid Gemini usage")
	}
	output := max(usageFields.Candidates+usageFields.Thoughts, usageFields.Total-usageFields.Prompt)
	return usageFields.Prompt, output, usageFields.Cached, nil
}
