package middleware

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/helper"
	"github.com/infinmalum/one-gateway/common/logger"
	"net/http"
	"strings"
)

func abortWithMessage(c *gin.Context, statusCode int, message string) {
	path := c.Request.URL.Path
	if path == "/v1/messages" {
		kind := "invalid_request_error"
		if statusCode == 401 {
			kind = "authentication_error"
		} else if statusCode >= 500 {
			kind = "api_error"
		}
		c.JSON(statusCode, gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
		c.Abort()
		logger.Error(c.Request.Context(), message)
		return
	}
	if isNativeGeminiRequest(c) {
		status := "INVALID_ARGUMENT"
		if statusCode == 401 {
			status = "UNAUTHENTICATED"
		} else if statusCode == 403 {
			status = "PERMISSION_DENIED"
		} else if statusCode >= 500 {
			status = "UNAVAILABLE"
		}
		c.JSON(statusCode, gin.H{"error": gin.H{"code": statusCode, "message": message, "status": status}})
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
	if modelAction := c.Param("modelAction"); modelAction != "" {
		if separator := strings.LastIndexByte(modelAction, ':'); separator > 0 {
			modelRequest.Model = modelAction[:separator]
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
