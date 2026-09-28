package lifecycle

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadUpstreamErrorKeepsStatusAndProviderMessage(t *testing.T) {
	for _, fixture := range []struct {
		body     string
		message  string
		typeName string
	}{
		{`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit"}}`, "rate limited", "rate_limit_error"},
		{`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, "busy", "overloaded_error"},
		{`{"message":"service unavailable"}`, "service unavailable", "upstream_error"},
		{`{"response":{"error":{"message":"nested failure"}}}`, "nested failure", "upstream_error"},
	} {
		response := &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(fixture.body))}
		result := ReadUpstreamError(response)
		if result.Status != http.StatusTooManyRequests || result.Message != fixture.message || result.Type != fixture.typeName {
			t.Fatalf("%s: unexpected parsed error %+v", fixture.body, result)
		}
	}
}
