package channeltype

import (
	"testing"

	"github.com/infinmalum/one-gateway/relay/apitype"
)

func TestAllOpenAIAdapterChannelsUseNativeChat(t *testing.T) {
	for kind := OpenAI; kind < Dummy; kind++ {
		if got, want := NativeChatCompatible(kind), ToAPIType(kind) == apitype.OpenAI; got != want {
			t.Errorf("channel type %d: native Chat eligibility %t, OpenAI adapter %t", kind, got, want)
		}
	}
}
