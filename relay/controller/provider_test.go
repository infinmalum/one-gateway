package controller

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/infinmalum/one-gateway/relay/adaptor"
	"github.com/infinmalum/one-gateway/relay/adaptor/openai"
	"github.com/infinmalum/one-gateway/relay/apitype"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/model"
	"github.com/stretchr/testify/require"
)

func TestPreserveProviderFieldsAfterModelMapping(t *testing.T) {
	converted, err := preserveExtraRequestFields(
		[]byte(`{"model":"alias","messages":[],"search_enabled":true,"extra_body":{"vendor_flag":1}}`),
		[]byte(`{"model":"upstream-model","messages":[]}`),
	)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(converted, &body))
	require.Equal(t, "upstream-model", body["model"])
	require.Equal(t, true, body["search_enabled"])
	require.Equal(t, float64(1), body["extra_body"].(map[string]any)["vendor_flag"])
}

func TestMappedOpenAIRequestKeepsProviderFields(t *testing.T) {
	transport := adaptor.NewContext(
		httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alias","search_enabled":true}`)),
		httptest.NewRecorder(),
	)
	transport.Request.Header.Set("Content-Type", "application/json")
	request := &model.TextRequest{Model: "upstream-model"}
	requestMeta := &meta.Meta{APIType: apitype.OpenAI, OriginModelName: "alias", ActualModelName: "upstream-model"}
	body, err := providerRequestBody(transport, []byte(`{"model":"alias","search_enabled":true}`), requestMeta, request, &openai.Adaptor{})
	require.NoError(t, err)
	encoded, err := io.ReadAll(body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, "upstream-model", decoded["model"])
	require.Equal(t, true, decoded["search_enabled"])
}
