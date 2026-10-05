package adaptor

import (
	"io"
	"net/http"

	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/model"
)

// RequestConverter has no HTTP-framework dependency. Provider-specific
// transports run behind the shared lifecycle through the same neutral
// transport Context the native paths use.
type RequestConverter interface {
	ConvertRequest(input *model.ConversionInput) (any, error)
	ConvertImageRequest(request *model.ImageRequest) (any, error)
}

type Adaptor interface {
	RequestConverter
	Init(meta *meta.Meta)
	GetRequestURL(meta *meta.Meta) (string, error)
	SetupRequestHeader(c *Context, req *http.Request, meta *meta.Meta) error
	DoRequest(c *Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error)
	DoResponse(c *Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode)
	GetModelList() []string
	GetChannelName() string
}
