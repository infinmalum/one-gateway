package native

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestErrorBodyUsesClientProtocol(t *testing.T) {
	for _, fixture := range []struct {
		protocol Protocol
		status   int
		want     string
	}{
		{Anthropic, http.StatusUnauthorized, `"authentication_error"`},
		{Anthropic, http.StatusForbidden, `"permission_error"`},
		{Anthropic, http.StatusTooManyRequests, `"rate_limit_error"`},
		{Gemini, http.StatusUnauthorized, `"UNAUTHENTICATED"`},
		{Gemini, http.StatusTooManyRequests, `"RESOURCE_EXHAUSTED"`},
		{Gemini, http.StatusServiceUnavailable, `"UNAVAILABLE"`},
		{OpenAIResponses, http.StatusUnauthorized, `"authentication_error"`},
		{OpenAIResponses, http.StatusTooManyRequests, `"rate_limit_error"`},
	} {
		body, err := json.Marshal(ErrorBody(fixture.protocol, fixture.status, "message"))
		if err != nil || !strings.Contains(string(body), fixture.want) {
			t.Fatalf("%s %d: %s (%v)", fixture.protocol, fixture.status, body, err)
		}
	}
}

func TestConvertedErrorKeepsUpstreamDetailsInsideClientEnvelope(t *testing.T) {
	for _, fixture := range []struct {
		protocol Protocol
		want     []string
	}{
		{OpenAIChat, []string{`"rate_limit_error"`, `"code":"overloaded"`, `"param":"model"`, `"upstream_type":"api_error"`}},
		{Anthropic, []string{`"type":"error"`, `"rate_limit_error"`, `"upstream_error"`, `"code":"overloaded"`, `"param":"model"`}},
	} {
		body, err := json.Marshal(ErrorBodyWithUpstream(fixture.protocol, http.StatusTooManyRequests, "busy", "api_error", "overloaded", "model"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range fixture.want {
			if !strings.Contains(string(body), want) {
				t.Fatalf("%s error lost %s: %s", fixture.protocol, want, body)
			}
		}
	}
}
