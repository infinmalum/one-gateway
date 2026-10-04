package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/relay/ginmeta"
)

// RelayProxy forwards an admin-only request to the target path on the
// explicitly selected channel's service. The route is intentionally unmetered.
// The target must stay on the channel's configured scheme and host: a target
// that names another host, scheme, or embedded credentials is rejected instead
// of followed, so channel credentials cannot be redirected elsewhere.
func RelayProxy(c *gin.Context) {
	metadata := ginmeta.Get(c)
	prefix := fmt.Sprintf("/v1/oneapi/proxy/%d", metadata.ChannelId)
	target := strings.TrimPrefix(c.Request.URL.Path, prefix)

	base, err := url.Parse(metadata.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		writeProxyError(c, http.StatusBadRequest, "invalid upstream base URL")
		return
	}
	joined, err := url.Parse(target)
	if err != nil || joined == nil {
		writeProxyError(c, http.StatusBadRequest, "invalid proxy target")
		return
	}
	if joined.Host != "" && !strings.EqualFold(joined.Host, base.Host) {
		writeProxyError(c, http.StatusBadRequest, "proxy target must stay on the channel host")
		return
	}
	if joined.Scheme != "" && !strings.EqualFold(joined.Scheme, base.Scheme) {
		writeProxyError(c, http.StatusBadRequest, "proxy target must stay on the channel scheme")
		return
	}
	upstream := *base
	upstream.Path = joined.Path
	upstream.RawPath = joined.RawPath
	upstream.RawQuery = c.Request.URL.RawQuery

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, upstream.String(), c.Request.Body)
	if err != nil {
		writeProxyError(c, http.StatusInternalServerError, err.Error())
		return
	}
	for name, values := range c.Request.Header {
		switch strings.ToLower(name) {
		case "host", "content-length", "accept-encoding", "connection", "authorization":
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Authorization", metadata.APIKey)

	providerClient := *client.HTTPClient
	providerClient.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 3 || next.URL.Scheme != previous[0].URL.Scheme || !strings.EqualFold(next.URL.Host, previous[0].URL.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := providerClient.Do(req)
	if err != nil {
		writeProxyError(c, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()
	for name, values := range resp.Header {
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func writeProxyError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{
		"message": message, "type": "one_api_error", "code": "proxy_error",
	}})
}
