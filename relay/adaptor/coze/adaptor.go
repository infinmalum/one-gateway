package coze

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/infinmalum/one-gateway/relay/adaptor"
	"github.com/infinmalum/one-gateway/relay/adaptor/openai"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/model"
)

type Adaptor struct {
	meta *meta.Meta
}

func (a *Adaptor) Init(meta *meta.Meta) {
	a.meta = meta
}

func (a *Adaptor) GetRequestURL(meta *meta.Meta) (string, error) {
	return fmt.Sprintf("%s/open_api/v2/chat", meta.BaseURL), nil
}

func (a *Adaptor) SetupRequestHeader(c *adaptor.Context, req *http.Request, meta *meta.Meta) error {
	adaptor.SetupCommonRequestHeader(c, req, meta)
	req.Header.Set("Authorization", "Bearer "+meta.APIKey)
	return nil
}

func (a *Adaptor) ConvertRequest(in *model.ConversionInput) (any, error) {
	request := in.Request
	if request == nil {
		return nil, errors.New("request is nil")
	}
	request.User = a.meta.Config.UserID
	return ConvertRequest(*request), nil
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

func (a *Adaptor) DoResponse(c *adaptor.Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode) {
	var responseText *string
	if meta.IsStream {
		err, responseText = StreamHandler(c, resp)
	} else {
		err, responseText = Handler(c, resp, meta.PromptTokens, meta.ActualModelName)
	}
	if responseText != nil {
		usage = openai.ResponseText2Usage(*responseText, meta.ActualModelName, meta.PromptTokens)
	} else {
		usage = &model.Usage{}
	}
	usage.PromptTokens = meta.PromptTokens
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return "coze"
}
