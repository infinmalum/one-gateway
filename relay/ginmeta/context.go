package ginmeta

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common/ctxkey"
	"github.com/infinmalum/one-gateway/model"
	"github.com/infinmalum/one-gateway/relay/channeltype"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/relaymode"
)

// Get is the Gin edge for the protocol-neutral request metadata shared by
// native and legacy relay paths.
func Get(c *gin.Context) *meta.Meta {
	result := &meta.Meta{
		Mode:        relaymode.GetByPath(c.Request.URL.Path),
		ChannelType: c.GetInt(ctxkey.Channel), ChannelId: c.GetInt(ctxkey.ChannelId),
		TokenId: c.GetInt(ctxkey.TokenId), TokenName: c.GetString(ctxkey.TokenName),
		UserId: c.GetInt(ctxkey.Id), Group: c.GetString(ctxkey.Group),
		ModelMapping:       c.GetStringMapString(ctxkey.ModelMapping),
		OriginModelName:    c.GetString(ctxkey.RequestModel),
		BaseURL:            c.GetString(ctxkey.BaseURL),
		APIKey:             strings.TrimPrefix(c.Request.Header.Get("Authorization"), "Bearer "),
		RequestURLPath:     c.Request.URL.String(),
		ForcedSystemPrompt: c.GetString(ctxkey.SystemPrompt), StartTime: time.Now(),
	}
	if config, ok := c.Get(ctxkey.Config); ok {
		result.Config = config.(model.ChannelConfig)
	}
	if result.BaseURL == "" {
		result.BaseURL = channeltype.ChannelBaseURLs[result.ChannelType]
	}
	result.APIType = channeltype.ToAPIType(result.ChannelType)
	return result
}
