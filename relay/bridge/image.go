package bridge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func chatInputBlocks(raw json.RawMessage, allowImage bool) ([]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		if plain == "" {
			return nil, nil
		}
		return []any{map[string]string{"type": "text", "text": plain}}, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, errors.New("Chat content must be text or content parts")
	}
	var result []any
	for _, rawPart := range parts {
		part, err := object(rawPart, "type", "text", "image_url")
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
			result = append(result, map[string]string{"type": "text", "text": value})
		case "image_url":
			if !allowImage || len(part) != 2 {
				return nil, errors.New("Chat image is only supported in user content")
			}
			image, err := chatImageToAnthropic(part["image_url"])
			if err != nil {
				return nil, err
			}
			result = append(result, image)
		default:
			return nil, fmt.Errorf("Chat content part %q cannot be converted", kind)
		}
	}
	return result, nil
}

func chatImageToAnthropic(raw json.RawMessage) (map[string]any, error) {
	image, err := object(raw, "url", "detail")
	if err != nil {
		return nil, err
	}
	if len(image["detail"]) != 0 {
		return nil, errors.New("Chat image detail cannot be converted")
	}
	var address string
	if json.Unmarshal(image["url"], &address) != nil {
		return nil, errors.New("Chat image_url requires a URL")
	}
	if strings.HasPrefix(address, "data:") {
		mediaType, data, err := parseImageDataURL(address)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": mediaType, "data": data}}, nil
	}
	if err := validateImageURL(address); err != nil {
		return nil, err
	}
	return map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": address}}, nil
}

func anthropicImageToChat(raw json.RawMessage) (map[string]any, error) {
	source, err := object(raw, "type", "url", "media_type", "data")
	if err != nil {
		return nil, err
	}
	var kind string
	_ = json.Unmarshal(source["type"], &kind)
	var address string
	switch kind {
	case "url":
		if len(source) != 2 || json.Unmarshal(source["url"], &address) != nil {
			return nil, errors.New("Anthropic URL image requires a URL")
		}
		if err := validateImageURL(address); err != nil {
			return nil, err
		}
	case "base64":
		var mediaType, data string
		if len(source) != 3 || json.Unmarshal(source["media_type"], &mediaType) != nil || json.Unmarshal(source["data"], &data) != nil || !supportedImageType(mediaType) || data == "" {
			return nil, errors.New("Anthropic base64 image requires a supported media type and data")
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return nil, errors.New("Anthropic image data must be base64")
		}
		address = "data:" + mediaType + ";base64," + data
	default:
		return nil, fmt.Errorf("Anthropic image source %q cannot be converted", kind)
	}
	return map[string]any{"type": "image_url", "image_url": map[string]string{"url": address}}, nil
}

func parseImageDataURL(address string) (string, string, error) {
	meta, data, ok := strings.Cut(strings.TrimPrefix(address, "data:"), ",")
	mediaType, encoding, hasEncoding := strings.Cut(meta, ";")
	if !ok || !hasEncoding || encoding != "base64" || !supportedImageType(mediaType) || data == "" {
		return "", "", errors.New("Chat image data URL requires supported base64 image data")
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return "", "", errors.New("Chat image data URL contains invalid base64")
	}
	return mediaType, data, nil
}

func supportedImageType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func validateImageURL(address string) error {
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("image URL must be an absolute HTTP URL without credentials")
	}
	return nil
}
