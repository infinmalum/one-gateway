package router

import (
	"github.com/infinmalum/one-gateway/controller"
	"github.com/infinmalum/one-gateway/middleware"

	"github.com/gin-gonic/gin"
)

func SetRelayRouter(router *gin.Engine) {
	router.Use(middleware.CORS())
	router.Use(middleware.GzipDecodeMiddleware())
	nativeOpenAI := router.Group("/v1")
	nativeOpenAI.Use(middleware.RelayPanicRecover(), middleware.TokenAuth())
	nativeOpenAI.POST("/responses", controller.NativeOpenAIResponses)
	nativeOpenAI.GET("/responses/:responseId", controller.NativeOpenAIResponsesRetrieve)
	nativeOpenAI.POST("/responses/:responseId/cancel", controller.NativeOpenAIResponsesCancel)
	nativeAnthropic := router.Group("/v1")
	nativeAnthropic.Use(middleware.RelayPanicRecover(), middleware.TokenAuth())
	nativeAnthropic.POST("/messages", controller.NativeAnthropic)
	for _, version := range []string{"/v1", "/v1beta"} {
		nativeGemini := router.Group(version)
		nativeGemini.Use(middleware.RelayPanicRecover(), middleware.TokenAuth())
		nativeGemini.POST("/models/:modelAction", controller.NativeGemini)
	}
	// https://platform.openai.com/docs/api-reference/introduction
	modelsRouter := router.Group("/v1/models")
	modelsRouter.Use(middleware.TokenAuth())
	{
		modelsRouter.GET("", controller.ListModels)
		modelsRouter.GET("/:model", controller.RetrieveModel)
	}
	relayV1Router := router.Group("/v1")
	relayV1Router.Use(middleware.RelayPanicRecover(), middleware.TokenAuth(), middleware.Distribute())
	{
		relayV1Router.Any("/oneapi/proxy/:channelid/*target", controller.RelayProxy)
		relayV1Router.POST("/completions", controller.RelayCompletions)
		relayV1Router.POST("/chat/completions", controller.RelayChat)
		relayV1Router.POST("/edits", controller.RelayEdits)
		relayV1Router.POST("/images/generations", controller.RelayImages)
		relayV1Router.POST("/images/edits", controller.RelayNotImplemented)
		relayV1Router.POST("/images/variations", controller.RelayNotImplemented)
		relayV1Router.POST("/embeddings", controller.RelayEmbeddings)
		relayV1Router.POST("/engines/:model/embeddings", controller.RelayEmbeddings)
		relayV1Router.POST("/audio/transcriptions", controller.RelayAudio)
		relayV1Router.POST("/audio/translations", controller.RelayAudio)
		relayV1Router.POST("/audio/speech", controller.RelayAudio)
		relayV1Router.GET("/files", controller.RelayNotImplemented)
		relayV1Router.POST("/files", controller.RelayNotImplemented)
		relayV1Router.DELETE("/files/:id", controller.RelayNotImplemented)
		relayV1Router.GET("/files/:id", controller.RelayNotImplemented)
		relayV1Router.GET("/files/:id/content", controller.RelayNotImplemented)
		relayV1Router.POST("/fine_tuning/jobs", controller.RelayNotImplemented)
		relayV1Router.GET("/fine_tuning/jobs", controller.RelayNotImplemented)
		relayV1Router.GET("/fine_tuning/jobs/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/fine_tuning/jobs/:id/cancel", controller.RelayNotImplemented)
		relayV1Router.GET("/fine_tuning/jobs/:id/events", controller.RelayNotImplemented)
		relayV1Router.DELETE("/models/:model", controller.RelayNotImplemented)
		relayV1Router.POST("/moderations", controller.RelayModerations)
		relayV1Router.POST("/assistants", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/assistants/:id", controller.RelayNotImplemented)
		relayV1Router.DELETE("/assistants/:id", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants", controller.RelayNotImplemented)
		relayV1Router.POST("/assistants/:id/files", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants/:id/files/:fileId", controller.RelayNotImplemented)
		relayV1Router.DELETE("/assistants/:id/files/:fileId", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants/:id/files", controller.RelayNotImplemented)
		relayV1Router.POST("/threads", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id", controller.RelayNotImplemented)
		relayV1Router.DELETE("/threads/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/messages", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/messages/:messageId", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/messages/:messageId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/messages/:messageId/files/:filesId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/messages/:messageId/files", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs/:runsId", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs/:runsId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs/:runsId/submit_tool_outputs", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs/:runsId/cancel", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs/:runsId/steps/:stepId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs/:runsId/steps", controller.RelayNotImplemented)
	}
}
