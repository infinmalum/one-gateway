package relay

import (
	"testing"

	"github.com/infinmalum/one-gateway/relay/apitype"
)

func TestGetAdaptor(t *testing.T) {
	for i := 0; i < apitype.Dummy; i++ {
		if i == apitype.Proxy || i == apitype.Dummy {
			// Proxy requests bypass adapters entirely; the sentinel type has
			// no adaptor.
			continue
		}
		if GetAdaptor(i) == nil {
			t.Errorf("api type %d has no adaptor", i)
		}
	}
}
