package channeltype

// NativeChatCompatible identifies channels whose Chat Completions endpoint
// accepts the OpenAI JSON and SSE wire format. Channels with provider-specific
// request conversion stay on their existing adapter until that conversion is
// moved into the shared lifecycle.
func NativeChatCompatible(kind int) bool {
	switch kind {
	case OpenAI, API2D, Azure, CloseAI, OpenAISB, OpenAIMax, OhMyGPT,
		Custom, Ails, AIProxy, API2GPT, AIGC2D, AI360, OpenRouter, FastGPT,
		Moonshot, Baichuan, Minimax, Mistral, Groq, LingYiWanWu,
		StepFun, DeepSeek, TogetherAI, Doubao, Novita, SiliconFlow,
		XAI, BaiduV2, XunfeiV2, AliBailian, OpenAICompatible,
		GeminiOpenAICompatible:
		return true
	default:
		return false
	}
}
