package client

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicContentAddressPolicy(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fc00::1", "::ffff:127.0.0.1"} {
		require.False(t, isPublicIP(netip.MustParseAddr(address)), address)
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		require.True(t, isPublicIP(netip.MustParseAddr(address)), address)
	}
}

func TestPublicContentClientNeverConnectsToLoopback(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_, err := NewPublicContentClient().Get(server.URL)
	require.Error(t, err)
	require.Zero(t, requests)
}

func TestPublicContentProxyPinsTargetIP(t *testing.T) {
	var target, host string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, host = r.URL.Host, r.Host
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	client := newPublicContentClient(proxyURL)
	response, err := client.Get("http://8.8.8.8/example.png")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, "8.8.8.8", target)
	require.Equal(t, "8.8.8.8", host)

	target = ""
	_, err = client.Get("http://127.0.0.1/private.png")
	require.Error(t, err)
	require.Empty(t, target)
}

func TestPublicContentProxyRejectsPrivateRedirect(t *testing.T) {
	requests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Location", "http://127.0.0.1/private.png")
		w.WriteHeader(http.StatusFound)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	_, err = newPublicContentClient(proxyURL).Get("http://8.8.8.8/image.png")
	require.Error(t, err)
	require.Equal(t, 1, requests)
}
