package lifecycle

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// UpstreamError retains provider error details without choosing the wire
// envelope returned to an OpenAI, Anthropic, or Gemini client.
type UpstreamError struct {
	Status  int
	Message string
	Type    string
	Code    any
	Param   string
}

func ReadUpstreamError(response *http.Response) UpstreamError {
	if response == nil {
		return UpstreamError{Status: http.StatusBadGateway, Message: "upstream response is missing", Type: "upstream_error", Code: "bad_response"}
	}
	result := UpstreamError{
		Status: response.StatusCode, Message: fmt.Sprintf("bad response status code %d", response.StatusCode),
		Type: "upstream_error", Code: "bad_response_status_code", Param: strconv.Itoa(response.StatusCode),
	}
	if response.Body == nil {
		return result
	}
	defer response.Body.Close()
	const maxError = 64 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, maxError+1))
	if err != nil || len(data) > maxError {
		return result
	}
	var envelope struct {
		Error    json.RawMessage `json:"error"`
		Message  string          `json:"message"`
		Msg      string          `json:"msg"`
		Err      string          `json:"err"`
		ErrorMsg string          `json:"error_msg"`
		Header   struct {
			Message string `json:"message"`
		} `json:"header"`
		Response struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return result
	}
	var detail struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Param   string `json:"param"`
	}
	if json.Unmarshal(envelope.Error, &detail) == nil && detail.Message != "" {
		result.Message = detail.Message
		if detail.Type != "" {
			result.Type = detail.Type
		}
		if detail.Code != nil {
			result.Code = detail.Code
		}
		if detail.Param != "" {
			result.Param = detail.Param
		}
		return result
	}
	var errorString string
	if json.Unmarshal(envelope.Error, &errorString) == nil && errorString != "" {
		result.Message = errorString
		return result
	}
	for _, message := range []string{envelope.Message, envelope.Msg, envelope.Err, envelope.ErrorMsg, envelope.Header.Message, envelope.Response.Error.Message} {
		if message != "" {
			result.Message = message
			break
		}
	}
	return result
}
