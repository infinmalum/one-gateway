package native

import "net/http"

// ErrorBody formats gateway errors in the protocol expected by the client.
// Provider error bodies are forwarded unchanged by the transport.
func ErrorBody(protocol Protocol, status int, message string) any {
	if protocol == OpenAIResponses {
		kind := "invalid_request_error"
		switch {
		case status == http.StatusUnauthorized:
			kind = "authentication_error"
		case status == http.StatusForbidden:
			kind = "permission_error"
		case status == http.StatusTooManyRequests:
			kind = "rate_limit_error"
		case status >= 500:
			kind = "server_error"
		}
		return map[string]any{"error": map[string]any{"message": message, "type": kind}}
	}
	if protocol == Anthropic {
		kind := "invalid_request_error"
		switch {
		case status == http.StatusUnauthorized:
			kind = "authentication_error"
		case status == http.StatusForbidden:
			kind = "permission_error"
		case status == http.StatusNotFound:
			kind = "not_found_error"
		case status == http.StatusTooManyRequests:
			kind = "rate_limit_error"
		case status >= 500:
			kind = "api_error"
		}
		return map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}}
	}
	statusName := "INVALID_ARGUMENT"
	switch {
	case status == http.StatusUnauthorized:
		statusName = "UNAUTHENTICATED"
	case status == http.StatusForbidden:
		statusName = "PERMISSION_DENIED"
	case status == http.StatusNotFound:
		statusName = "NOT_FOUND"
	case status == http.StatusTooManyRequests:
		statusName = "RESOURCE_EXHAUSTED"
	case status == http.StatusServiceUnavailable || status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		statusName = "UNAVAILABLE"
	case status >= 500:
		statusName = "INTERNAL"
	}
	return map[string]any{"error": map[string]any{"code": status, "message": message, "status": statusName}}
}
