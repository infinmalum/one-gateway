package aws

import (
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/infinmalum/one-gateway/common/ctxkey"

	"github.com/infinmalum/one-gateway/relay/adaptor"
	"github.com/infinmalum/one-gateway/relay/adaptor/aws/utils"
	"github.com/infinmalum/one-gateway/relay/meta"
	"github.com/infinmalum/one-gateway/relay/model"
	"github.com/pkg/errors"
)

var _ utils.AwsAdapter = new(Adaptor)

type Adaptor struct {
}

func (a *Adaptor) ConvertRequest(in *model.ConversionInput) (any, error) {
	request := in.Request
	if request == nil {
		return nil, errors.New("request is nil")
	}

	llamaReq := ConvertRequest(*request)
	in.Set(ctxkey.RequestModel, request.Model)
	in.Set(ctxkey.ConvertedRequest, llamaReq)
	return llamaReq, nil
}

func (a *Adaptor) DoResponse(c *adaptor.Context, awsCli *bedrockruntime.Client, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode) {
	if meta.IsStream {
		err, usage = StreamHandler(c, awsCli)
	} else {
		err, usage = Handler(c, awsCli, meta.ActualModelName)
	}
	return
}
