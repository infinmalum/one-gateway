package vertexai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/infinmalum/one-gateway/relay/adaptor"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/model"
)

var _ adaptor.Adaptor = new(Adaptor)

const channelName = "vertexai"

type Adaptor struct{}

func (a *Adaptor) Init(meta *meta.Meta) {
}

func (a *Adaptor) ConvertRequest(in *model.ConversionInput) (any, error) {
	request := in.Request
	if request == nil {
		return nil, errors.New("request is nil")
	}

	inner := GetAdaptor(request.Model)
	if inner == nil {
		return nil, errors.New("adaptor not found")
	}

	return inner.ConvertRequest(in)
}

func (a *Adaptor) DoResponse(c *adaptor.Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode) {
	inner := GetAdaptor(meta.ActualModelName)
	if inner == nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error: model.Error{
				Message: "adaptor not found",
			},
		}
	}
	return inner.DoResponse(c, resp, meta)
}

func (a *Adaptor) GetModelList() (models []string) {
	models = modelList
	return
}

func (a *Adaptor) GetChannelName() string {
	return channelName
}

func (a *Adaptor) GetRequestURL(meta *meta.Meta) (string, error) {
	suffix := ""
	if strings.HasPrefix(meta.ActualModelName, "gemini") {
		if meta.IsStream {
			suffix = "streamGenerateContent?alt=sse"
		} else {
			suffix = "generateContent"
		}
	} else {
		if meta.IsStream {
			suffix = "streamRawPredict?alt=sse"
		} else {
			suffix = "rawPredict"
		}
	}

	if meta.BaseURL != "" {
		return fmt.Sprintf(
			"%s/v1/projects/%s/locations/%s/publishers/google/models/%s:%s",
			meta.BaseURL,
			meta.Config.VertexAIProjectID,
			meta.Config.Region,
			meta.ActualModelName,
			suffix,
		), nil
	}
	return fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:%s",
		meta.Config.Region,
		meta.Config.VertexAIProjectID,
		meta.Config.Region,
		meta.ActualModelName,
		suffix,
	), nil
}

func (a *Adaptor) SetupRequestHeader(c *adaptor.Context, req *http.Request, meta *meta.Meta) error {
	adaptor.SetupCommonRequestHeader(c, req, meta)
	token, err := getToken(c.Request.Context(), meta.ChannelId, meta.Config.VertexAIADC)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (a *Adaptor) ConvertImageRequest(request *model.ImageRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	return request, nil
}

func (a *Adaptor) DoRequest(c *adaptor.Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error) {
	return adaptor.DoRequestHelper(a, c, meta, requestBody)
}
