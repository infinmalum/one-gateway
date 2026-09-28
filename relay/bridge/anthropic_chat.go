package bridge

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/infinmalum/one-gateway/relay/native"
)

// AnthropicToChatRequest converts the Messages subset that Chat Completions
// can represent. Unsupported fields fail before the upstream call. Streaming
// requests convert their body; the response stream uses the paired
// StreamConverter.
func AnthropicToChatRequest(body []byte, model string) ([]byte, error) {
	if model == "" {
		return nil, errors.New("model is required")
	}
	fields, err := object(body, "model", "messages", "max_tokens", "system", "stream", "temperature", "top_p", "stop_sequences", "tools", "tool_choice")
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
	converted := map[string]any{"model": model, "max_completion_tokens": limit}
	if stream {
		converted["stream"] = true
		converted["stream_options"] = map[string]bool{"include_usage": true}
	}
	var chatMessages []any
	if len(fields["system"]) != 0 {
		text, err := messagesText(fields["system"])
		if err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
		chatMessages = append(chatMessages, map[string]any{"role": "system", "content": text})
	}
	for _, raw := range messages {
		message, err := object(raw, "role", "content")
		if err != nil {
			return nil, err
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil || (role != "user" && role != "assistant") {
			return nil, errors.New("Messages role must be user or assistant")
		}
		var plain string
		if json.Unmarshal(message["content"], &plain) == nil {
			chatMessages = append(chatMessages, map[string]any{"role": role, "content": plain})
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil || len(blocks) == 0 {
			return nil, errors.New("Messages content must be text or a non-empty block array")
		}
		var textParts []any
		var toolCalls []any
		flushUserText := func() {
			if len(textParts) > 0 {
				chatMessages = append(chatMessages, map[string]any{"role": "user", "content": textParts})
				textParts = nil
			}
		}
		for _, blockRaw := range blocks {
			block, err := object(blockRaw, "type", "text", "source", "id", "name", "input", "tool_use_id", "content", "is_error")
			if err != nil {
				return nil, err
			}
			var kind string
			_ = json.Unmarshal(block["type"], &kind)
			switch kind {
			case "text":
				if len(block) != 2 || (role == "assistant" && len(toolCalls) > 0) {
					return nil, errors.New("text block has fields that Chat cannot represent")
				}
				var text string
				if json.Unmarshal(block["text"], &text) != nil {
					return nil, errors.New("text block text must be a string")
				}
				textParts = append(textParts, map[string]string{"type": "text", "text": text})
			case "image":
				if role != "user" || len(block) != 2 {
					return nil, errors.New("image must be a user block with only a source")
				}
				part, err := anthropicImageToChat(block["source"])
				if err != nil {
					return nil, err
				}
				textParts = append(textParts, part)
			case "tool_use":
				if role != "assistant" || len(block) != 4 {
					return nil, errors.New("tool_use must be an assistant block with id, name, and input")
				}
				var id, name string
				_ = json.Unmarshal(block["id"], &id)
				_ = json.Unmarshal(block["name"], &name)
				var input map[string]any
				if id == "" || name == "" || json.Unmarshal(block["input"], &input) != nil || input == nil {
					return nil, errors.New("tool_use requires an object input")
				}
				arguments, _ := json.Marshal(input)
				toolCalls = append(toolCalls, map[string]any{"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": string(arguments)}})
			case "tool_result":
				if role != "user" || len(block) != 3 {
					return nil, errors.New("tool_result cannot be represented in this message")
				}
				var id string
				_ = json.Unmarshal(block["tool_use_id"], &id)
				content, err := messagesText(block["content"])
				if id == "" || err != nil {
					return nil, errors.New("tool_result requires a tool_use_id and text content")
				}
				flushUserText()
				chatMessages = append(chatMessages, map[string]any{"role": "tool", "tool_call_id": id, "content": content})
			default:
				return nil, fmt.Errorf("Messages content block %q cannot be converted", kind)
			}
		}
		if role == "assistant" {
			message := map[string]any{"role": "assistant", "content": nil}
			if len(textParts) > 0 {
				message["content"] = textParts
			}
			if len(toolCalls) > 0 {
				message["tool_calls"] = toolCalls
			}
			if len(textParts) == 0 && len(toolCalls) == 0 {
				return nil, errors.New("assistant content is empty")
			}
			chatMessages = append(chatMessages, message)
		} else {
			flushUserText()
		}
	}
	converted["messages"] = chatMessages
	for _, key := range []string{"temperature", "top_p"} {
		if len(fields[key]) != 0 {
			converted[key] = json.RawMessage(fields[key])
		}
	}
	if len(fields["stop_sequences"]) != 0 {
		var stop []string
		if json.Unmarshal(fields["stop_sequences"], &stop) != nil {
			return nil, errors.New("stop_sequences must be an array of strings")
		}
		converted["stop"] = stop
	}
	if len(fields["tools"]) != 0 {
		var tools []json.RawMessage
		if json.Unmarshal(fields["tools"], &tools) != nil {
			return nil, errors.New("tools must be an array")
		}
		var chatTools []any
		for _, raw := range tools {
			tool, err := object(raw, "name", "description", "input_schema")
			if err != nil {
				return nil, err
			}
			var name string
			_ = json.Unmarshal(tool["name"], &name)
			var schema map[string]any
			if name == "" || json.Unmarshal(tool["input_schema"], &schema) != nil || schema == nil {
				return nil, errors.New("client tool requires a name and object input_schema")
			}
			function := map[string]any{"name": name, "parameters": schema}
			if len(tool["description"]) != 0 {
				function["description"] = json.RawMessage(tool["description"])
			}
			chatTools = append(chatTools, map[string]any{"type": "function", "function": function})
		}
		converted["tools"] = chatTools
	}
	if len(fields["tool_choice"]) != 0 {
		choice, err := object(fields["tool_choice"], "type", "name")
		if err != nil {
			return nil, err
		}
		var kind string
		_ = json.Unmarshal(choice["type"], &kind)
		switch kind {
		case "auto", "none":
			if len(choice) != 1 {
				return nil, errors.New("tool_choice has unsupported fields")
			}
			converted["tool_choice"] = kind
		case "any":
			if len(choice) != 1 {
				return nil, errors.New("tool_choice has unsupported fields")
			}
			converted["tool_choice"] = "required"
		case "tool":
			var name string
			_ = json.Unmarshal(choice["name"], &name)
			if name == "" || len(choice) != 2 {
				return nil, errors.New("named tool_choice requires a name")
			}
			converted["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": name}}
		default:
			return nil, fmt.Errorf("tool_choice %q cannot be converted", kind)
		}
	}
	return json.Marshal(converted)
}

func ChatToAnthropicResponse(body []byte, requestedModel string) ([]byte, native.Usage, error) {
	fields, err := object(body, "id", "object", "created", "model", "choices", "usage")
	if err != nil {
		return nil, native.Usage{}, err
	}
	var id string
	_ = json.Unmarshal(fields["id"], &id)
	var objectType string
	_ = json.Unmarshal(fields["object"], &objectType)
	var choices []json.RawMessage
	if id == "" || objectType != "chat.completion" || json.Unmarshal(fields["choices"], &choices) != nil || len(choices) != 1 {
		return nil, native.Usage{}, errors.New("Chat response requires an id, chat.completion object, and one choice")
	}
	choice, err := object(choices[0], "index", "message", "finish_reason", "logprobs")
	if err != nil {
		return nil, native.Usage{}, err
	}
	if len(choice["logprobs"]) != 0 && string(choice["logprobs"]) != "null" {
		return nil, native.Usage{}, errors.New("Chat logprobs cannot be converted")
	}
	var index int
	if json.Unmarshal(choice["index"], &index) != nil || index != 0 {
		return nil, native.Usage{}, errors.New("Chat response choice index must be zero")
	}
	var finish string
	_ = json.Unmarshal(choice["finish_reason"], &finish)
	stop := ""
	switch finish {
	case "stop":
		stop = "end_turn"
	case "length":
		stop = "max_tokens"
	case "tool_calls":
		stop = "tool_use"
	default:
		return nil, native.Usage{}, fmt.Errorf("Chat finish_reason %q cannot be converted", finish)
	}
	message, err := object(choice["message"], "role", "content", "tool_calls")
	if err != nil {
		return nil, native.Usage{}, err
	}
	var role string
	_ = json.Unmarshal(message["role"], &role)
	if role != "assistant" {
		return nil, native.Usage{}, errors.New("Chat response message must be assistant")
	}
	blocks, err := textBlocks(message["content"])
	if err != nil {
		return nil, native.Usage{}, err
	}
	if len(message["tool_calls"]) != 0 {
		var calls []json.RawMessage
		if json.Unmarshal(message["tool_calls"], &calls) != nil {
			return nil, native.Usage{}, errors.New("tool_calls must be an array")
		}
		for _, raw := range calls {
			call, err := object(raw, "id", "type", "function")
			if err != nil {
				return nil, native.Usage{}, err
			}
			var callID, kind string
			_ = json.Unmarshal(call["id"], &callID)
			_ = json.Unmarshal(call["type"], &kind)
			function, err := object(call["function"], "name", "arguments")
			if err != nil || callID == "" || kind != "function" {
				return nil, native.Usage{}, errors.New("only function tool calls can be converted")
			}
			var name, arguments string
			_ = json.Unmarshal(function["name"], &name)
			_ = json.Unmarshal(function["arguments"], &arguments)
			var input map[string]any
			if name == "" || json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
				return nil, native.Usage{}, errors.New("tool call arguments must be a JSON object")
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": callID, "name": name, "input": input})
		}
	}
	if len(blocks) == 0 {
		return nil, native.Usage{}, errors.New("Chat response content is empty")
	}
	if len(fields["usage"]) == 0 || string(fields["usage"]) == "null" {
		return nil, native.Usage{}, errors.New("Chat response usage is required for Messages conversion")
	}
	usageFields, err := object(fields["usage"], "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details")
	if err != nil {
		return nil, native.Usage{}, err
	}
	if len(usageFields["prompt_tokens_details"]) != 0 {
		if _, err := object(usageFields["prompt_tokens_details"], "cached_tokens"); err != nil {
			return nil, native.Usage{}, err
		}
	}
	var source struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		PromptDetails    struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if json.Unmarshal(fields["usage"], &source) != nil || len(usageFields["prompt_tokens"]) == 0 || len(usageFields["completion_tokens"]) == 0 || source.PromptDetails.CachedTokens < 0 || source.PromptTokens < source.PromptDetails.CachedTokens || source.PromptTokens < 0 || source.CompletionTokens < 0 {
		return nil, native.Usage{}, errors.New("invalid Chat usage")
	}
	usage := native.Usage{Input: source.PromptTokens, Output: source.CompletionTokens, Seen: true, Complete: true}
	messageUsage := map[string]int64{"input_tokens": source.PromptTokens - source.PromptDetails.CachedTokens, "cache_read_input_tokens": source.PromptDetails.CachedTokens, "output_tokens": source.CompletionTokens}
	result := map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": requestedModel,
		"content": blocks, "stop_reason": stop, "stop_sequence": nil, "usage": messageUsage,
	}
	converted, err := json.Marshal(result)
	return converted, usage, err
}

func messagesText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", errors.New("content must be text or text blocks")
	}
	for _, blockRaw := range blocks {
		block, err := object(blockRaw, "type", "text")
		if err != nil {
			return "", err
		}
		var kind, part string
		_ = json.Unmarshal(block["type"], &kind)
		if kind != "text" || json.Unmarshal(block["text"], &part) != nil {
			return "", errors.New("only text blocks can be converted")
		}
		text += part
	}
	return text, nil
}
