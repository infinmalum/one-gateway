package controller

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/stretchr/testify/require"
)

func TestPreserveProviderFieldsAfterModelMapping(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alias","messages":[],"search_enabled":true,"extra_body":{"vendor_flag":1}}`))
	converted, err := preserveExtraRequestFields(context, []byte(`{"model":"upstream-model","messages":[]}`))
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(converted, &body))
	require.Equal(t, "upstream-model", body["model"])
	require.Equal(t, true, body["search_enabled"])
	require.Equal(t, float64(1), body["extra_body"].(map[string]any)["vendor_flag"])
}

func TestMappedOpenAIRequestKeepsProviderFields(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alias","search_enabled":true}`))
	context.Request.Header.Set("Content-Type", "application/json")
	request := &model.GeneralOpenAIRequest{Model: "upstream-model"}
	metadata := &meta.Meta{APIType: apitype.OpenAI, OriginModelName: "alias", ActualModelName: "upstream-model"}
	body, err := getRequestBody(context, metadata, request, &openai.Adaptor{})
	require.NoError(t, err)
	encoded, err := io.ReadAll(body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, "upstream-model", decoded["model"])
	require.Equal(t, true, decoded["search_enabled"])
}
