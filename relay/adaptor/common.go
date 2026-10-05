package adaptor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/infinmalum/one-gateway/common/client"
	"github.com/infinmalum/one-gateway/relay/meta"
)

func SetupCommonRequestHeader(c *Context, req *http.Request, meta *meta.Meta) {
	req.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	req.Header.Set("Accept", c.Request.Header.Get("Accept"))
	if meta.IsStream && c.Request.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
}

func DoRequestHelper(a Adaptor, c *Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error) {
	fullRequestURL, err := a.GetRequestURL(meta)
	if err != nil {
		return nil, fmt.Errorf("get request url failed: %w", err)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, fullRequestURL, requestBody)
	if err != nil {
		return nil, fmt.Errorf("new request failed: %w", err)
	}
	err = a.SetupRequestHeader(c, req, meta)
	if err != nil {
		return nil, fmt.Errorf("setup request header failed: %w", err)
	}
	resp, err := DoRequest(c, req)
	if err != nil {
		return nil, fmt.Errorf("do request failed: %w", err)
	}
	return resp, nil
}

func DoRequest(c *Context, req *http.Request) (*http.Response, error) {
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	providerClient := *httpClient
	providerClient.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 3 || next.URL.Scheme != previous[0].URL.Scheme || !strings.EqualFold(next.URL.Host, previous[0].URL.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := providerClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("resp is nil")
	}
	_ = req.Body.Close()
	_ = c.Request.Body.Close()
	return resp, nil
}
