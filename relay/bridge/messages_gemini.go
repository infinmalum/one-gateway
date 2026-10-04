package bridge

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/infinmalum/one-gateway/relay/native"
)

// MessagesToGemini converts the Messages subset that Gemini GenerateContent
// can represent: text, function tools and calls, base64 images and PDF
// documents, and thinking blocks. Unrepresentable fields fail before the
// upstream call. Streaming requests convert their body; the upstream stream
// action is selected by the transport.
func MessagesToGemini(body []byte, model string) ([]byte, error) {
	if model == "" {
		return nil, errors.New("model is required")
	}
	fields, err := object(body, "model", "messages", "max_tokens", "system", "stream", "temperature", "top_p", "top_k", "stop_sequences", "tools", "tool_choice")
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
	if len(fields["tools"]) != 0 {
		tools, err := messagesToolsToGemini(fields["tools"])
		if err != nil {
			return nil, err
		}
		request["tools"] = tools
	}
	if len(fields["tool_choice"]) != 0 {
		toolConfig, err := messagesToolChoiceToGemini(fields["tool_choice"])
		if err != nil {
			return nil, err
		}
		request["toolConfig"] = map[string]any{"functionCallingConfig": toolConfig}
	}
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
	toolNames := map[string]string{}
	for _, raw := range messages {
		message, err := object(raw, "role", "content")
		if err != nil {
			return nil, err
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil || (role != "user" && role != "assistant") {
			return nil, errors.New("Messages role must be user or assistant")
		}
		parts, err := messagesBlocksToGemini(message["content"], role == "assistant", toolNames)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			return nil, errors.New("Gemini conversion requires message content")
		}
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		appendGeminiContent(&contents, geminiRole, parts)
	}
	request["contents"] = contents
	request["generationConfig"] = config
	return json.Marshal(request)
}

func messagesBlocksToGemini(raw json.RawMessage, assistant bool, toolNames map[string]string) ([]map[string]any, error) {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		if plain == "" {
			return nil, errors.New("Gemini conversion requires text message content")
		}
		return []map[string]any{{"text": plain}}, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return nil, errors.New("Messages content must be text or a non-empty block array")
	}
	var parts []map[string]any
	for _, blockRaw := range blocks {
		block, err := object(blockRaw, "type", "text", "source", "id", "name", "input", "tool_use_id", "content", "thinking", "signature")
		if err != nil {
			return nil, err
		}
		var kind string
		_ = json.Unmarshal(block["type"], &kind)
		switch kind {
		case "text":
			if len(block) != 2 {
				return nil, errors.New("text block has fields that Gemini cannot represent")
			}
			var text string
			if json.Unmarshal(block["text"], &text) != nil {
				return nil, errors.New("text block text must be a string")
			}
			parts = append(parts, map[string]any{"text": text})
		case "image":
			if assistant {
				return nil, errors.New("assistant messages cannot contain image blocks")
			}
			media, err := anthropicMediaToGemini(block["source"], false)
			if err != nil {
				return nil, err
			}
			parts = append(parts, media)
		case "document":
			if assistant {
				return nil, errors.New("assistant messages cannot contain document blocks")
			}
			media, err := anthropicMediaToGemini(block["source"], true)
			if err != nil {
				return nil, err
			}
			parts = append(parts, media)
		case "thinking":
			if !assistant {
				return nil, errors.New("thinking blocks belong to assistant messages")
			}
			var text string
			if json.Unmarshal(block["thinking"], &text) != nil {
				return nil, errors.New("thinking block requires thinking text")
			}
			thought := map[string]any{"text": text, "thought": true}
			if signature := rawString(block["signature"]); signature != "" {
				thought["thoughtSignature"] = signature
			}
			parts = append(parts, thought)
		case "redacted_thinking":
			return nil, errors.New("redacted thinking cannot be converted to Gemini")
		case "tool_use":
			if !assistant || len(block) != 4 {
				return nil, errors.New("tool_use must be an assistant block with id, name, and input")
			}
			var id, name string
			_ = json.Unmarshal(block["id"], &id)
			_ = json.Unmarshal(block["name"], &name)
			var input map[string]any
			if id == "" || name == "" || json.Unmarshal(block["input"], &input) != nil || input == nil {
				return nil, errors.New("tool_use requires an object input")
			}
			if existing, ok := toolNames[id]; ok && existing != name {
				return nil, errors.New("tool_use id maps to conflicting function names")
			}
			toolNames[id] = name
			functionCall := map[string]any{"name": name, "args": input, "id": id}
			parts = append(parts, map[string]any{"functionCall": functionCall})
		case "tool_result":
			if assistant || len(block) != 3 {
				return nil, errors.New("tool_result must be a user block with tool_use_id and content")
			}
			var id string
			_ = json.Unmarshal(block["tool_use_id"], &id)
			name := toolNames[id]
			if id == "" || name == "" {
				return nil, errors.New("tool_result references an unknown tool_use id")
			}
			content, err := messagesText(block["content"])
			if err != nil {
				return nil, err
			}
			response, err := geminiToolResponseObject(content)
			if err != nil {
				return nil, err
			}
			functionResponse := map[string]any{"id": id, "name": name, "response": response}
			parts = append(parts, map[string]any{"functionResponse": functionResponse})
		default:
			return nil, fmt.Errorf("Messages content block %q cannot be converted", kind)
		}
	}
	return parts, nil
}

// GeminiToMessages converts one Gemini candidate into an Anthropic message.
// Function calls become tool_use blocks and thought parts become thinking
// blocks carrying the thought signature. Provider metadata is kept in a named
// extension while response content and finish states Messages cannot
// represent are rejected.
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
	content, err := object(candidate["content"], "role", "parts")
	if err != nil {
		return nil, native.Usage{}, err
	}
	if role := rawString(content["role"]); role != "" && role != "model" {
		return nil, native.Usage{}, errors.New("Gemini response content role must be model")
	}
	var rawParts []json.RawMessage
	if json.Unmarshal(content["parts"], &rawParts) != nil || len(rawParts) == 0 {
		return nil, native.Usage{}, errors.New("Gemini response requires content parts")
	}
	var blocks []any
	var toolCalls bool
	openThinking := -1
	closeThinking := func() {
		openThinking = -1
	}
	for _, raw := range rawParts {
		part, err := parseGeminiResponsePart(raw)
		if err != nil {
			return nil, native.Usage{}, err
		}
		switch part.kind {
		case geminiPartText:
			blocks = append(blocks, map[string]string{"type": "text", "text": part.text})
			closeThinking()
		case geminiPartThought:
			block := map[string]any{"type": "thinking", "thinking": part.text}
			if part.signature != "" {
				block["signature"] = part.signature
			}
			blocks = append(blocks, block)
			openThinking = len(blocks) - 1
		case geminiPartSignature:
			if openThinking < 0 {
				return nil, native.Usage{}, errors.New("Gemini thought signature has no preceding thinking block")
			}
			blocks[openThinking].(map[string]any)["signature"] = part.signature
		case geminiPartFunctionCall:
			toolCalls = true
			blockID := part.callID
			if blockID == "" {
				generated, err := newStreamID("toolu_")
				if err != nil {
					return nil, native.Usage{}, err
				}
				blockID = generated
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": blockID, "name": part.name, "input": json.RawMessage(part.args)})
			closeThinking()
		}
	}
	if len(blocks) == 0 {
		return nil, native.Usage{}, errors.New("Gemini response requires content")
	}
	stop, err := mapGeminiStopReason(finish, toolCalls)
	if err != nil {
		return nil, native.Usage{}, err
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
		"content":     blocks,
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
