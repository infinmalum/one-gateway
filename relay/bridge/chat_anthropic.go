package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/infinmalum/one-gateway/relay/native"
)

// ChatToAnthropic converts the representable synchronous Chat Completions
// subset. It rejects fields that would otherwise be silently discarded.
func ChatToAnthropic(body []byte, model string) ([]byte, error) {
	if model == "" {
		return nil, errors.New("model is required")
	}
	fields, err := object(body, "model", "messages", "stream", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop", "tools", "tool_choice")
	if err != nil {
		return nil, err
	}
	var stream bool
	if err := decode(fields["stream"], &stream); err != nil || stream {
		return nil, errors.New("streaming Chat to Anthropic conversion is not supported")
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(fields["messages"], &messages); err != nil || len(messages) == 0 {
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
	limit := max(maxTokens, maxCompletion)
	if limit == 0 {
		limit = 1024
	}
	request := map[string]any{"model": model, "max_tokens": limit}
	var system []string
	var converted []any
	for _, raw := range messages {
		message, err := object(raw, "role", "content", "tool_calls", "tool_call_id")
		if err != nil {
			return nil, fmt.Errorf("invalid Chat message: %w", err)
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil {
			return nil, errors.New("Chat message role is required")
		}
		switch role {
		case "system", "developer":
			var text string
			if err := json.Unmarshal(message["content"], &text); err != nil {
				return nil, errors.New("system and developer content must be text for Anthropic conversion")
			}
			if len(converted) > 0 {
				return nil, errors.New("system and developer messages must precede conversation messages")
			}
			system = append(system, text)
		case "user", "assistant":
			if len(message["tool_call_id"]) != 0 {
				return nil, errors.New("tool_call_id is only valid on tool messages")
			}
			blocks, err := textBlocks(message["content"])
			if err != nil {
				return nil, err
			}
			if len(message["tool_calls"]) != 0 {
				if role != "assistant" {
					return nil, errors.New("tool_calls require an assistant message")
				}
				var calls []json.RawMessage
				if err := json.Unmarshal(message["tool_calls"], &calls); err != nil {
					return nil, errors.New("tool_calls must be an array")
				}
				for _, call := range calls {
					item, err := object(call, "id", "type", "function")
					if err != nil {
						return nil, err
					}
					var id, kind string
					_ = json.Unmarshal(item["id"], &id)
					_ = json.Unmarshal(item["type"], &kind)
					if id == "" || kind != "function" {
						return nil, errors.New("only named function tool calls can be converted")
					}
					function, err := object(item["function"], "name", "arguments")
					if err != nil {
						return nil, err
					}
					var name, arguments string
					_ = json.Unmarshal(function["name"], &name)
					_ = json.Unmarshal(function["arguments"], &arguments)
					var input map[string]any
					if name == "" || json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
						return nil, errors.New("function tool arguments must be a JSON object")
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
				}
			}
			if len(blocks) == 0 {
				return nil, errors.New("Chat message content is empty")
			}
			converted = append(converted, map[string]any{"role": role, "content": blocks})
		case "tool":
			if len(message["tool_calls"]) != 0 {
				return nil, errors.New("tool messages cannot contain tool_calls")
			}
			var id, content string
			_ = json.Unmarshal(message["tool_call_id"], &id)
			if id == "" || json.Unmarshal(message["content"], &content) != nil {
				return nil, errors.New("tool messages require tool_call_id and text content")
			}
			converted = append(converted, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}}})
		default:
			return nil, fmt.Errorf("Chat role %q is not supported by Anthropic conversion", role)
		}
	}
	request["messages"] = converted
	if len(system) > 0 {
		request["system"] = joinSystem(system)
	}
	for _, key := range []string{"temperature", "top_p"} {
		if len(fields[key]) != 0 {
			request[key] = json.RawMessage(fields[key])
		}
	}
	if len(fields["stop"]) != 0 {
		var one string
		if json.Unmarshal(fields["stop"], &one) == nil {
			request["stop_sequences"] = []string{one}
		} else {
			var many []string
			if json.Unmarshal(fields["stop"], &many) != nil {
				return nil, errors.New("stop must be text or a list of text")
			}
			request["stop_sequences"] = many
		}
	}
	if len(fields["tools"]) != 0 {
		var tools []json.RawMessage
		if json.Unmarshal(fields["tools"], &tools) != nil {
			return nil, errors.New("tools must be an array")
		}
		var convertedTools []any
		for _, raw := range tools {
			tool, err := object(raw, "type", "function")
			if err != nil {
				return nil, err
			}
			var kind string
			_ = json.Unmarshal(tool["type"], &kind)
			if kind != "function" {
				return nil, errors.New("only function tools can be converted")
			}
			function, err := object(tool["function"], "name", "description", "parameters")
			if err != nil {
				return nil, err
			}
			var name string
			_ = json.Unmarshal(function["name"], &name)
			if name == "" {
				return nil, errors.New("function tool name is required")
			}
			var schema map[string]any
			if json.Unmarshal(function["parameters"], &schema) != nil || schema == nil {
				return nil, errors.New("function tool parameters must be a JSON object")
			}
			convertedTool := map[string]any{"name": name, "input_schema": json.RawMessage(function["parameters"])}
			if len(function["description"]) != 0 {
				convertedTool["description"] = json.RawMessage(function["description"])
			}
			convertedTools = append(convertedTools, convertedTool)
		}
		request["tools"] = convertedTools
	}
	if len(fields["tool_choice"]) != 0 {
		var choice string
		if json.Unmarshal(fields["tool_choice"], &choice) == nil {
			switch choice {
			case "auto", "none":
				request["tool_choice"] = map[string]string{"type": choice}
			case "required":
				request["tool_choice"] = map[string]string{"type": "any"}
			default:
				return nil, errors.New("unsupported tool_choice")
			}
		} else {
			choiceObject, err := object(fields["tool_choice"], "type", "function")
			if err != nil {
				return nil, err
			}
			var kind string
			_ = json.Unmarshal(choiceObject["type"], &kind)
			function, err := object(choiceObject["function"], "name")
			if err != nil || kind != "function" {
				return nil, errors.New("only named function tool_choice can be converted")
			}
			var name string
			_ = json.Unmarshal(function["name"], &name)
			if name == "" {
				return nil, errors.New("named function tool_choice requires a name")
			}
			request["tool_choice"] = map[string]string{"type": "tool", "name": name}
		}
	}
	return json.Marshal(request)
}

func AnthropicToChat(body []byte, requestedModel string) ([]byte, native.Usage, error) {
	var response struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Type != "message" || response.ID == "" {
		return nil, native.Usage{}, errors.New("invalid Anthropic message response")
	}
	finish := ""
	switch response.StopReason {
	case "end_turn", "stop_sequence":
		finish = "stop"
	case "max_tokens":
		finish = "length"
	case "tool_use":
		finish = "tool_calls"
	default:
		return nil, native.Usage{}, fmt.Errorf("Anthropic stop reason %q cannot be converted", response.StopReason)
	}
	message := map[string]any{"role": "assistant", "content": nil}
	var text string
	var toolCalls []any
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			text += block.Text
		case "tool_use":
			if block.ID == "" || block.Name == "" || len(block.Input) == 0 {
				return nil, native.Usage{}, errors.New("invalid Anthropic tool_use block")
			}
			arguments, err := json.Marshal(json.RawMessage(block.Input))
			if err != nil {
				return nil, native.Usage{}, err
			}
			toolCalls = append(toolCalls, map[string]any{"id": block.ID, "type": "function", "function": map[string]string{"name": block.Name, "arguments": string(arguments)}})
		default:
			return nil, native.Usage{}, fmt.Errorf("Anthropic content block %q cannot be converted", block.Type)
		}
	}
	if text != "" {
		message["content"] = text
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	usage := native.Usage{Complete: true}
	if response.Usage != nil {
		usage.Input = response.Usage.InputTokens + response.Usage.CacheCreationInputTokens + response.Usage.CacheReadInputTokens
		usage.Output = response.Usage.OutputTokens
		usage.Seen = true
	}
	result := map[string]any{
		"id": response.ID, "object": "chat.completion", "created": time.Now().Unix(), "model": requestedModel,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	}
	if usage.Seen {
		result["usage"] = map[string]int64{"prompt_tokens": usage.Input, "completion_tokens": usage.Output, "total_tokens": usage.Input + usage.Output}
	}
	converted, err := json.Marshal(result)
	return converted, usage, err
}

func object(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, errors.New("expected a JSON object")
	}
	permitted := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		permitted[key] = true
	}
	for key := range value {
		if !permitted[key] {
			return nil, fmt.Errorf("field %q cannot be converted", key)
		}
	}
	return value, nil
}

func decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func textBlocks(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []any{map[string]string{"type": "text", "text": text}}, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, errors.New("Chat content must be text or text parts")
	}
	var result []any
	for _, part := range parts {
		item, err := object(part, "type", "text")
		if err != nil {
			return nil, err
		}
		var kind, text string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["text"], &text)
		if kind != "text" {
			return nil, fmt.Errorf("Chat content part %q cannot be converted", kind)
		}
		result = append(result, map[string]string{"type": "text", "text": text})
	}
	return result, nil
}

func joinSystem(parts []string) string {
	result := parts[0]
	for _, part := range parts[1:] {
		result += "\n\n" + part
	}
	return result
}
