package adaptor

import (
	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/model"
	"io"
	"net/http"
)

// RequestConverter has no HTTP-framework dependency. Provider-specific
// transports may still use the legacy Gin adaptor until phase 5.
type RequestConverter interface {
	ConvertRequest(input *model.ConversionInput) (any, error)
	ConvertImageRequest(request *model.ImageRequest) (any, error)
}

type Adaptor interface {
	RequestConverter
	Init(meta *meta.Meta)
	GetRequestURL(meta *meta.Meta) (string, error)
	SetupRequestHeader(c *gin.Context, req *http.Request, meta *meta.Meta) error
	DoRequest(c *gin.Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error)
	DoResponse(c *gin.Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode)
	GetModelList() []string
	GetChannelName() string
}
