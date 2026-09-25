package relay

import (
	"github.com/infinmalum/one-gateway/relay/adaptor"
	"github.com/infinmalum/one-gateway/relay/adaptor/aiproxy"
	"github.com/infinmalum/one-gateway/relay/adaptor/ali"
	"github.com/infinmalum/one-gateway/relay/adaptor/anthropic"
	"github.com/infinmalum/one-gateway/relay/adaptor/aws"
	"github.com/infinmalum/one-gateway/relay/adaptor/baidu"
	"github.com/infinmalum/one-gateway/relay/adaptor/cloudflare"
	"github.com/infinmalum/one-gateway/relay/adaptor/cohere"
	"github.com/infinmalum/one-gateway/relay/adaptor/coze"
	"github.com/infinmalum/one-gateway/relay/adaptor/deepl"
	"github.com/infinmalum/one-gateway/relay/adaptor/gemini"
	"github.com/infinmalum/one-gateway/relay/adaptor/ollama"
	"github.com/infinmalum/one-gateway/relay/adaptor/openai"
	"github.com/infinmalum/one-gateway/relay/adaptor/palm"
	"github.com/infinmalum/one-gateway/relay/adaptor/proxy"
	"github.com/infinmalum/one-gateway/relay/adaptor/replicate"
	"github.com/infinmalum/one-gateway/relay/adaptor/tencent"
	"github.com/infinmalum/one-gateway/relay/adaptor/vertexai"
	"github.com/infinmalum/one-gateway/relay/adaptor/xunfei"
	"github.com/infinmalum/one-gateway/relay/adaptor/zhipu"
	"github.com/infinmalum/one-gateway/relay/apitype"
)

func GetAdaptor(apiType int) adaptor.Adaptor {
	switch apiType {
	case apitype.AIProxyLibrary:
		return &aiproxy.Adaptor{}
	case apitype.Ali:
		return &ali.Adaptor{}
	case apitype.Anthropic:
		return &anthropic.Adaptor{}
	case apitype.AwsClaude:
		return &aws.Adaptor{}
	case apitype.Baidu:
		return &baidu.Adaptor{}
	case apitype.Gemini:
		return &gemini.Adaptor{}
	case apitype.OpenAI:
		return &openai.Adaptor{}
	case apitype.PaLM:
		return &palm.Adaptor{}
	case apitype.Tencent:
		return &tencent.Adaptor{}
	case apitype.Xunfei:
		return &xunfei.Adaptor{}
	case apitype.Zhipu:
		return &zhipu.Adaptor{}
	case apitype.Ollama:
		return &ollama.Adaptor{}
	case apitype.Coze:
		return &coze.Adaptor{}
	case apitype.Cohere:
		return &cohere.Adaptor{}
	case apitype.Cloudflare:
		return &cloudflare.Adaptor{}
	case apitype.DeepL:
		return &deepl.Adaptor{}
	case apitype.VertexAI:
		return &vertexai.Adaptor{}
	case apitype.Proxy:
		return &proxy.Adaptor{}
	case apitype.Replicate:
		return &replicate.Adaptor{}
	}
	return nil
}
