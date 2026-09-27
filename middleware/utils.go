package middleware

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/helper"
	"github.com/infinmalum/one-gateway/common/logger"
	"github.com/infinmalum/one-gateway/relay/native"
	"net/http"
	"strings"
)

func abortWithMessage(c *gin.Context, statusCode int, message string) {
	path := c.Request.URL.Path
	if path == "/v1/messages" {
		c.JSON(statusCode, native.ErrorBody(native.Anthropic, statusCode, message))
		c.Abort()
		logger.Error(c.Request.Context(), message)
		return
	}
	if path == "/v1/responses" {
		c.JSON(statusCode, native.ErrorBody(native.OpenAIResponses, statusCode, message))
		c.Abort()
		logger.Error(c.Request.Context(), message)
		return
	}
	if isNativeGeminiRequest(c) {
		c.JSON(statusCode, native.ErrorBody(native.Gemini, statusCode, message))
		c.Abort()
		logger.Error(c.Request.Context(), message)
		return
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": helper.MessageWithRequestId(message, c.GetString(helper.RequestIdKey)),
			"type":    "one_api_error",
		},
	})
	c.Abort()
	logger.Error(c.Request.Context(), message)
}

func isNativeGeminiRequest(c *gin.Context) bool {
	return c.Request.Method == http.MethodPost && c.Param("modelAction") != "" &&
		(strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") || strings.HasPrefix(c.Request.URL.Path, "/v1/models/"))
}

func getRequestModel(c *gin.Context) (string, error) {
	if isNativeGeminiRequest(c) {
		modelAction := c.Param("modelAction")
		if separator := strings.LastIndexByte(modelAction, ':'); separator > 0 {
			return modelAction[:separator], nil
		}
		return "", fmt.Errorf("Gemini model and action are required")
	}
	if c.Request.URL.Path == "/v1/messages" || c.Request.URL.Path == "/v1/responses" {
		body, err := common.GetRequestBody(c)
		if err != nil {
			return "", err
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return "", err
		}
		if request.Model == "" {
			return "", fmt.Errorf("model is required")
		}
		return request.Model, nil
	}
	var modelRequest ModelRequest
	err := common.UnmarshalBodyReusable(c, &modelRequest)
	if err != nil {
		return "", fmt.Errorf("common.UnmarshalBodyReusable failed: %w", err)
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/moderations") {
		if modelRequest.Model == "" {
			modelRequest.Model = "text-moderation-stable"
		}
	}
	if strings.HasSuffix(c.Request.URL.Path, "embeddings") {
		if pathModel := c.Param("model"); pathModel != "" && modelRequest.Model != "" && modelRequest.Model != pathModel {
			return "", fmt.Errorf("body model does not match the engine path")
		}
		if modelRequest.Model == "" {
			modelRequest.Model = c.Param("model")
		}
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/images/generations") {
		if modelRequest.Model == "" {
			modelRequest.Model = "dall-e-2"
		}
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/audio/transcriptions") || strings.HasPrefix(c.Request.URL.Path, "/v1/audio/translations") {
		if modelRequest.Model == "" {
			modelRequest.Model = "whisper-1"
		}
	}
	return modelRequest.Model, nil
}

func isModelInList(modelName string, models string) bool {
	modelList := strings.Split(models, ",")
	for _, model := range modelList {
		if modelName == model {
			return true
		}
	}
	return false
}
