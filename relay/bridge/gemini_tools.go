package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
)

// appendGeminiContent merges parts into contents, combining consecutive
// entries with the same role the way GenerateContent expects tool results to
// follow one another.
func appendGeminiContent(contents *[]any, role string, parts []map[string]any) {
	if len(parts) == 0 {
		return
	}
	if len(*contents) > 0 {
		if last, ok := (*contents)[len(*contents)-1].(map[string]any); ok && last["role"] == role {
			existing, _ := last["parts"].([]map[string]any)
			last["parts"] = append(existing, parts...)
			return
		}
	}
	*contents = append(*contents, map[string]any{"role": role, "parts": parts})
}

// chatToolsToGemini converts OpenAI function tools into Gemini function
// declarations. Tool types without a function schema cannot be represented.
func chatToolsToGemini(raw json.RawMessage) ([]any, error) {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil, errors.New("tools must be an array")
	}
	var declarations []any
	for _, item := range tools {
		tool, err := object(item, "type", "function")
		if err != nil {
			return nil, err
		}
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind != "function" {
			return nil, errors.New("only function tools can be converted")
		}
		function, err := object(tool["function"], "name", "description", "parameters", "strict")
		if err != nil {
			return nil, err
		}
		var name string
		_ = json.Unmarshal(function["name"], &name)
		if name == "" {
			return nil, errors.New("function tool name is required")
		}
		if len(function["strict"]) != 0 && string(function["strict"]) != "null" {
			return nil, errors.New("function tool strict mode cannot be converted")
		}
		declaration := map[string]any{"name": name}
		if len(function["description"]) != 0 {
			declaration["description"] = json.RawMessage(function["description"])
		}
		if len(function["parameters"]) != 0 && string(function["parameters"]) != "null" {
			var schema map[string]any
			if json.Unmarshal(function["parameters"], &schema) != nil || schema == nil {
				return nil, errors.New("function tool parameters must be a JSON object")
			}
			declaration["parameters"] = json.RawMessage(function["parameters"])
		}
		declarations = append(declarations, declaration)
	}
	if len(declarations) == 0 {
		return nil, errors.New("tools must not be empty")
	}
	return []any{map[string]any{"functionDeclarations": declarations}}, nil
}

// messagesToolsToGemini converts Anthropic client tools into Gemini function
// declarations.
func messagesToolsToGemini(raw json.RawMessage) ([]any, error) {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil, errors.New("tools must be an array")
	}
	var declarations []any
	for _, item := range tools {
		tool, err := object(item, "name", "description", "input_schema")
		if err != nil {
			return nil, err
		}
		var name string
		_ = json.Unmarshal(tool["name"], &name)
		if name == "" {
			return nil, errors.New("client tool requires a name")
		}
		declaration := map[string]any{"name": name}
		if len(tool["description"]) != 0 {
			declaration["description"] = json.RawMessage(tool["description"])
		}
		if len(tool["input_schema"]) != 0 && string(tool["input_schema"]) != "null" {
			var schema map[string]any
			if json.Unmarshal(tool["input_schema"], &schema) != nil || schema == nil {
				return nil, errors.New("client tool input_schema must be a JSON object")
			}
			declaration["parameters"] = json.RawMessage(tool["input_schema"])
		}
		declarations = append(declarations, declaration)
	}
	if len(declarations) == 0 {
		return nil, errors.New("tools must not be empty")
	}
	return []any{map[string]any{"functionDeclarations": declarations}}, nil
}

// chatToolChoiceToGemini converts the Chat tool_choice values Gemini's
// function calling config can represent.
func chatToolChoiceToGemini(raw json.RawMessage) (map[string]any, error) {
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto":
			return map[string]any{"mode": "AUTO"}, nil
		case "none":
			return map[string]any{"mode": "NONE"}, nil
		case "required":
			return map[string]any{"mode": "ANY"}, nil
		default:
			return nil, fmt.Errorf("tool_choice %q cannot be converted", choice)
		}
	}
	choiceObject, err := object(raw, "type", "function")
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
	return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{name}}, nil
}

// messagesToolChoiceToGemini converts the Messages tool_choice values
// Gemini's function calling config can represent.
func messagesToolChoiceToGemini(raw json.RawMessage) (map[string]any, error) {
	choice, err := object(raw, "type", "name")
	if err != nil {
		return nil, err
	}
	var kind string
	_ = json.Unmarshal(choice["type"], &kind)
	switch kind {
	case "auto":
		if len(choice) != 1 {
			return nil, errors.New("tool_choice has unsupported fields")
		}
		return map[string]any{"mode": "AUTO"}, nil
	case "none":
		if len(choice) != 1 {
			return nil, errors.New("tool_choice has unsupported fields")
		}
		return map[string]any{"mode": "NONE"}, nil
	case "any":
		if len(choice) != 1 {
			return nil, errors.New("tool_choice has unsupported fields")
		}
		return map[string]any{"mode": "ANY"}, nil
	case "tool":
		var name string
		_ = json.Unmarshal(choice["name"], &name)
		if name == "" || len(choice) != 2 {
			return nil, errors.New("named tool_choice requires a name")
		}
		return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{name}}, nil
	default:
		return nil, fmt.Errorf("tool_choice %q cannot be converted", kind)
	}
}

// geminiToolResponseObject converts tool result text into the JSON object
// Gemini's functionResponse.response requires. Structured results pass
// through; text results use the conventional result key.
func geminiToolResponseObject(content string) (map[string]any, error) {
	var parsed map[string]any
	if json.Unmarshal([]byte(content), &parsed) == nil && parsed != nil {
		return parsed, nil
	}
	return map[string]any{"result": content}, nil
}
