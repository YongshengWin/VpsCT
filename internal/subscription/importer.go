package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ctlvps/internal/proxynode"
)

// DefaultUserAgent mimics a Clash client so airports return YAML with userinfo.
const DefaultUserAgent = "clash.meta/1.19.0 (ctlvps)"

// FetchResult is what an external subscription returned.
type FetchResult struct {
	Body        string
	ContentType string
	Userinfo    Userinfo
	HasUserinfo bool
	Proxies     []proxynode.Proxy
	Format      string
	ParseErrors []string
	Filename    string
}

// Fetcher downloads and parses subscriptions.
type Fetcher struct {
	Client *http.Client
}

// NewFetcher builds a fetcher with sane timeouts and no redirects to
// non-http schemes.
func NewFetcher() *Fetcher {
	return &Fetcher{Client: &http.Client{Timeout: 45 * time.Second}}
}

// Fetch downloads url with the given user agent and parses the body.
func (f *Fetcher) Fetch(ctx context.Context, url, userAgent string) (*FetchResult, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("订阅地址必须以 http:// 或 https:// 开头")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	res := ParseBody(string(body))
	res.ContentType = resp.Header.Get("Content-Type")
	res.Userinfo, res.HasUserinfo = ParseUserinfo(resp.Header.Get("Subscription-Userinfo"))
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := strings.Index(cd, "filename="); i >= 0 {
			res.Filename = strings.Trim(cd[i+len("filename="):], `"' `)
		}
	}
	return res, nil
}

// ParseBody parses subscription text without network access.
func ParseBody(body string) *FetchResult {
	pr := proxynode.ParseAny(body)
	res := &FetchResult{Body: body, Format: pr.Format, ParseErrors: pr.Errors}
	res.Proxies = proxynode.DedupeNames(pr.Proxies)
	return res
}
