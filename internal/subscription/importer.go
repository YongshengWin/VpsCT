package subscription

import (
	"context"
	"errors"
	"fmt"

	"ctlvps/internal/safehttp"
	"net/http"
	"strings"

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
var fetchGate = safehttp.Gate{Limit: 4}

type Fetcher struct {
	Client *http.Client
}

// NewFetcher builds a fetcher with sane timeouts and no redirects to
// non-http schemes.
func NewFetcher() *Fetcher {
	return &Fetcher{Client: safehttp.New(safehttp.Options{})}
}

// Fetch downloads url with the given user agent and parses the body.
func (f *Fetcher) Fetch(ctx context.Context, url, userAgent string) (*FetchResult, error) {
	if !fetchGate.Acquire() {
		return nil, errors.New("外部订阅同步繁忙，请稍后重试")
	}
	defer fetchGate.Release()
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("订阅地址必须以 http:// 或 https:// 开头")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("订阅地址无效")
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, errors.New("外部订阅请求失败或目标不符合访问策略")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := safehttp.ReadBounded(resp.Body, 16<<20)
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
