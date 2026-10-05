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

// ChatToGemini converts the Chat Completions subset that Gemini
// GenerateContent can represent: text, function tools and calls, base64
// images and audio input, and thought-free responses. Fields without a
// Gemini representation fail before the upstream call. Streaming requests
// convert their body; the upstream stream action is selected by the
// transport.
func ChatToGemini(body []byte, model string) ([]byte, error) {
	if model == "" {
		return nil, errors.New("model is required")
	}
	fields, err := object(body, "model", "messages", "stream", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop", "tools", "tool_choice")
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
	if len(fields["tools"]) != 0 {
		tools, err := chatToolsToGemini(fields["tools"])
		if err != nil {
			return nil, err
		}
		request["tools"] = tools
	}
	if len(fields["tool_choice"]) != 0 {
		toolConfig, err := chatToolChoiceToGemini(fields["tool_choice"])
		if err != nil {
			return nil, err
		}
		request["toolConfig"] = map[string]any{"functionCallingConfig": toolConfig}
	}
	var contents []any
	var system []string
	toolNames := map[string]string{}
	for _, raw := range messages {
		message, err := object(raw, "role", "content", "tool_calls", "tool_call_id")
		if err != nil {
			return nil, fmt.Errorf("invalid Chat message: %w", err)
		}
		var role string
		_ = json.Unmarshal(message["role"], &role)
		switch role {
		case "system":
			if len(contents) != 0 {
				return nil, errors.New("system messages must precede conversation messages")
			}
			parts, err := geminiTextParts(message["content"])
			if err != nil {
				return nil, err
			}
			for _, part := range parts {
				text, _ := part["text"].(string)
				system = append(system, text)
			}
		case "user":
			if len(message["tool_calls"]) != 0 {
				return nil, errors.New("user messages cannot contain tool_calls")
			}
			if len(message["tool_call_id"]) != 0 {
				return nil, errors.New("tool_call_id is only valid on tool messages")
			}
			parts, err := chatUserPartsToGemini(message["content"])
			if err != nil {
				return nil, err
			}
			appendGeminiContent(&contents, "user", parts)
		case "assistant":
			if len(message["tool_call_id"]) != 0 {
				return nil, errors.New("tool_call_id is only valid on tool messages")
			}
			parts, err := geminiTextParts(message["content"])
			if err != nil {
				return nil, err
			}
			var calls []json.RawMessage
			if len(message["tool_calls"]) != 0 {
				if json.Unmarshal(message["tool_calls"], &calls) != nil {
					return nil, errors.New("tool_calls must be an array")
				}
			}
			if len(calls) > 0 {
				parts = keepNonEmptyText(parts)
			}
			for _, call := range calls {
				part, err := chatToolCallToGemini(call, toolNames)
				if err != nil {
					return nil, err
				}
				parts = append(parts, part)
			}
			if len(parts) == 0 {
				return nil, errors.New("Chat message content is empty")
			}
			appendGeminiContent(&contents, "model", parts)
		case "tool":
			if len(message["tool_calls"]) != 0 {
				return nil, errors.New("tool messages cannot contain tool_calls")
			}
			part, err := chatToolResultToGemini(message, toolNames)
			if err != nil {
				return nil, err
			}
			appendGeminiContent(&contents, "user", []map[string]any{part})
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

func chatToolCallToGemini(call json.RawMessage, toolNames map[string]string) (map[string]any, error) {
	item, err := object(call, "id", "type", "function")
	if err != nil {
		return nil, err
	}
	var id, kind string
	_ = json.Unmarshal(item["id"], &id)
	_ = json.Unmarshal(item["type"], &kind)
	function, err := object(item["function"], "name", "arguments")
	if err != nil {
		return nil, err
	}
	var name, arguments string
	_ = json.Unmarshal(function["name"], &name)
	_ = json.Unmarshal(function["arguments"], &arguments)
	var input map[string]any
	if name == "" || kind != "function" || json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
		return nil, errors.New("function tool arguments must be a JSON object")
	}
	if id != "" {
		if existing, ok := toolNames[id]; ok && existing != name {
			return nil, errors.New("tool_call id maps to conflicting function names")
		}
		toolNames[id] = name
	}
	functionCall := map[string]any{"name": name, "args": input}
	if id != "" {
		functionCall["id"] = id
	}
	return map[string]any{"functionCall": functionCall}, nil
}

func chatToolResultToGemini(message map[string]json.RawMessage, toolNames map[string]string) (map[string]any, error) {
	var id, content string
	_ = json.Unmarshal(message["tool_call_id"], &id)
	if id == "" || json.Unmarshal(message["content"], &content) != nil {
		return nil, errors.New("tool messages require tool_call_id and text content")
	}
	name := toolNames[id]
	if name == "" {
		return nil, errors.New("tool message references an unknown tool_call id")
	}
	response, err := geminiToolResponseObject(content)
	if err != nil {
		return nil, err
	}
	return map[string]any{"functionResponse": map[string]any{"id": id, "name": name, "response": response}}, nil
}

func keepNonEmptyText(parts []map[string]any) []map[string]any {
	var kept []map[string]any
	for _, part := range parts {
		if part["text"] != "" {
			kept = append(kept, part)
		}
	}
	return kept
}

func chatUserPartsToGemini(raw json.RawMessage) ([]map[string]any, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []map[string]any{{"text": text}}, nil
	}
	var rawParts []json.RawMessage
	if json.Unmarshal(raw, &rawParts) != nil || len(rawParts) == 0 {
		return nil, errors.New("Gemini conversion requires text content")
	}
	var parts []map[string]any
	for _, rawPart := range rawParts {
		part, err := object(rawPart, "type", "text", "image_url", "input_audio")
		if err != nil {
			return nil, err
		}
		var kind string
		_ = json.Unmarshal(part["type"], &kind)
		switch kind {
		case "text":
			var value string
			if len(part) != 2 || json.Unmarshal(part["text"], &value) != nil {
				return nil, errors.New("Chat text part requires text")
			}
			parts = append(parts, map[string]any{"text": value})
		case "image_url":
			if len(part) != 2 {
				return nil, errors.New("Chat image part requires only image_url")
			}
			media, err := chatImageToGemini(part["image_url"])
			if err != nil {
				return nil, err
			}
			parts = append(parts, media)
		case "input_audio":
			if len(part) != 2 {
				return nil, errors.New("Chat audio part requires only input_audio")
			}
			media, err := chatAudioToGemini(part["input_audio"])
			if err != nil {
				return nil, err
			}
			parts = append(parts, media)
		default:
			return nil, fmt.Errorf("Chat content part %q cannot be converted", kind)
		}
	}
	return parts, nil
}

func geminiTextParts(raw json.RawMessage) ([]map[string]any, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []map[string]any{{"text": text}}, nil
	}
	var rawParts []json.RawMessage
	if json.Unmarshal(raw, &rawParts) != nil || len(rawParts) == 0 {
		return nil, errors.New("Gemini conversion requires text content")
	}
	var parts []map[string]any
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
		parts = append(parts, map[string]any{"text": value})
	}
	return parts, nil
}

// GeminiToChat keeps provider metadata in a named response extension while
// rejecting response content and finish states Chat cannot represent.
// Function calls become tool calls with the upstream call ID when present;
// thought parts have no Chat representation and fail.
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
		return nil, native.Usage{}, errors.New("Gemini response requires content parts")
	}
	var text string
	var toolCalls []any
	for _, raw := range rawParts {
		part, err := parseGeminiResponsePart(raw)
		if err != nil {
			return nil, native.Usage{}, err
		}
		switch part.kind {
		case geminiPartText:
			text += part.text
		case geminiPartFunctionCall:
			callID := part.callID
			if callID == "" {
				generated, err := newStreamID("call_")
				if err != nil {
					return nil, native.Usage{}, err
				}
				callID = generated
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": callID, "type": "function",
				"function": map[string]string{"name": part.name, "arguments": string(part.args)},
			})
		default:
			return nil, native.Usage{}, errors.New("Gemini response contains a part Chat cannot represent")
		}
	}
	if text == "" && len(toolCalls) == 0 {
		return nil, native.Usage{}, errors.New("Gemini response requires content")
	}
	chatFinish, err := mapGeminiFinishReason(finish, len(toolCalls) > 0)
	if err != nil {
		return nil, native.Usage{}, err
	}
	prompt, output, cached, err := geminiUsage(fields["usageMetadata"], true)
	if err != nil {
		return nil, native.Usage{}, err
	}
	usage := native.Usage{Input: prompt, Output: output, Seen: true, Complete: true}
	message := map[string]any{"role": "assistant", "content": nil}
	if text != "" {
		message["content"] = text
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	result := map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": requestedModel,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": chatFinish}},
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
