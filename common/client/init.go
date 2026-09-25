package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/common/logger"
)

var HTTPClient *http.Client
var ImpatientHTTPClient *http.Client
var UserContentRequestHTTPClient *http.Client

func Init() {
	var contentProxy *url.URL
	if config.UserContentRequestProxy != "" {
		var err error
		contentProxy, err = url.Parse(config.UserContentRequestProxy)
		if err != nil || contentProxy.Scheme != "http" || contentProxy.Host == "" {
			logger.FatalLog("USER_CONTENT_REQUEST_PROXY must be a valid HTTP proxy URL")
		}
	}
	UserContentRequestHTTPClient = newPublicContentClient(contentProxy)
	var transport http.RoundTripper
	if config.RelayProxy != "" {
		logger.SysLog(fmt.Sprintf("using %s as api relay proxy", config.RelayProxy))
		proxyURL, err := url.Parse(config.RelayProxy)
		if err != nil {
			logger.FatalLog(fmt.Sprintf("USER_CONTENT_REQUEST_PROXY set but invalid: %s", config.UserContentRequestProxy))
		}
		transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}

	if config.RelayTimeout == 0 {
		HTTPClient = &http.Client{
			Transport: transport,
		}
	} else {
		HTTPClient = &http.Client{
			Timeout:   time.Duration(config.RelayTimeout) * time.Second,
			Transport: transport,
		}
	}

	ImpatientHTTPClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
	}
}

// NewPublicContentClient connects only to public IP addresses.
func NewPublicContentClient() *http.Client {
	return newPublicContentClient(nil)
}

func newPublicContentClient(proxyURL *url.URL) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolvePublicIPs(ctx, host)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	var roundTripper http.RoundTripper = transport
	if proxyURL != nil {
		roundTripper = &publicProxyTransport{proxyURL: proxyURL}
	}
	return &http.Client{
		Transport: roundTripper,
		Timeout:   time.Second * time.Duration(config.UserContentRequestTimeout),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("unsupported user content URL scheme")
			}
			return nil
		},
	}
}

// publicProxyTransport sends an IP literal to the proxy. This prevents the
// proxy from resolving a checked hostname to a different, private address.
type publicProxyTransport struct {
	proxyURL *url.URL
}

func (p *publicProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, errors.New("unsupported user content URL scheme")
	}
	addresses, err := resolvePublicIPs(req.Context(), req.URL.Hostname())
	if err != nil {
		return nil, err
	}
	port := req.URL.Port()
	if port == "" {
		if req.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	forwarded := req.Clone(req.Context())
	forwarded.URL.Host = net.JoinHostPort(addresses[0].String(), port)
	forwarded.Host = req.URL.Host
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(p.proxyURL)
	transport.TLSClientConfig = &tls.Config{ServerName: req.URL.Hostname(), MinVersion: tls.VersionTLS12}
	response, err := transport.RoundTrip(forwarded)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &closeProxyBody{ReadCloser: response.Body, transport: transport}
	return response, nil
}

type closeProxyBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *closeProxyBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}

func resolvePublicIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("user content URL has no IP address")
	}
	for _, address := range addresses {
		if !isPublicIP(address) {
			return nil, errors.New("user content URL resolves to a non-public address")
		}
	}
	return addresses, nil
}

var blockedPublicContentRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPublicContentRanges {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
