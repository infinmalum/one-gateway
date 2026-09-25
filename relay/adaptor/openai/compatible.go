package openai

import (
	"github.com/infinmalum/one-gateway/relay/adaptor/ai360"
	"github.com/infinmalum/one-gateway/relay/adaptor/alibailian"
	"github.com/infinmalum/one-gateway/relay/adaptor/baichuan"
	"github.com/infinmalum/one-gateway/relay/adaptor/baiduv2"
	"github.com/infinmalum/one-gateway/relay/adaptor/deepseek"
	"github.com/infinmalum/one-gateway/relay/adaptor/doubao"
	"github.com/infinmalum/one-gateway/relay/adaptor/geminiv2"
	"github.com/infinmalum/one-gateway/relay/adaptor/groq"
	"github.com/infinmalum/one-gateway/relay/adaptor/lingyiwanwu"
	"github.com/infinmalum/one-gateway/relay/adaptor/minimax"
	"github.com/infinmalum/one-gateway/relay/adaptor/mistral"
	"github.com/infinmalum/one-gateway/relay/adaptor/moonshot"
	"github.com/infinmalum/one-gateway/relay/adaptor/novita"
	"github.com/infinmalum/one-gateway/relay/adaptor/openrouter"
	"github.com/infinmalum/one-gateway/relay/adaptor/siliconflow"
	"github.com/infinmalum/one-gateway/relay/adaptor/stepfun"
	"github.com/infinmalum/one-gateway/relay/adaptor/togetherai"
	"github.com/infinmalum/one-gateway/relay/adaptor/xai"
	"github.com/infinmalum/one-gateway/relay/adaptor/xunfeiv2"
	"github.com/infinmalum/one-gateway/relay/channeltype"
)

var CompatibleChannels = []int{
	channeltype.Azure,
	channeltype.AI360,
	channeltype.Moonshot,
	channeltype.Baichuan,
	channeltype.Minimax,
	channeltype.Doubao,
	channeltype.Mistral,
	channeltype.Groq,
	channeltype.LingYiWanWu,
	channeltype.StepFun,
	channeltype.DeepSeek,
	channeltype.TogetherAI,
	channeltype.Novita,
	channeltype.SiliconFlow,
	channeltype.XAI,
	channeltype.BaiduV2,
	channeltype.XunfeiV2,
}

func GetCompatibleChannelMeta(channelType int) (string, []string) {
	switch channelType {
	case channeltype.Azure:
		return "azure", ModelList
	case channeltype.AI360:
		return "360", ai360.ModelList
	case channeltype.Moonshot:
		return "moonshot", moonshot.ModelList
	case channeltype.Baichuan:
		return "baichuan", baichuan.ModelList
	case channeltype.Minimax:
		return "minimax", minimax.ModelList
	case channeltype.Mistral:
		return "mistralai", mistral.ModelList
	case channeltype.Groq:
		return "groq", groq.ModelList
	case channeltype.LingYiWanWu:
		return "lingyiwanwu", lingyiwanwu.ModelList
	case channeltype.StepFun:
		return "stepfun", stepfun.ModelList
	case channeltype.DeepSeek:
		return "deepseek", deepseek.ModelList
	case channeltype.TogetherAI:
		return "together.ai", togetherai.ModelList
	case channeltype.Doubao:
		return "doubao", doubao.ModelList
	case channeltype.Novita:
		return "novita", novita.ModelList
	case channeltype.SiliconFlow:
		return "siliconflow", siliconflow.ModelList
	case channeltype.XAI:
		return "xai", xai.ModelList
	case channeltype.BaiduV2:
		return "baiduv2", baiduv2.ModelList
	case channeltype.XunfeiV2:
		return "xunfeiv2", xunfeiv2.ModelList
	case channeltype.OpenRouter:
		return "openrouter", openrouter.ModelList
	case channeltype.AliBailian:
		return "alibailian", alibailian.ModelList
	case channeltype.GeminiOpenAICompatible:
		return "geminiv2", geminiv2.ModelList
	default:
		return "openai", ModelList
	}
}
