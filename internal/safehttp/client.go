// Package safehttp implements a DNS-pinned, bounded HTTP client for untrusted URLs.
package safehttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrTarget = errors.New("目标地址不符合出站访问策略")
var ErrSize = errors.New("响应超过大小限制")

// Options are local administrator policy, never request-controlled.
type Options struct {
	HTTPOrigins []string
	// PrivateOrigins grants an exact origin access to private IPs. Redirects
	// receive no inherited exception. Metadata and non-unicast remain denied.
	PrivateOrigins []string
}

var forbidden = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func AllowedIP(ip netip.Addr, private bool) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return private
	}
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range forbidden {
		if p.Contains(ip) {
			return false
		}
	}
	if ip.IsPrivate() || ip.IsLoopback() {
		return private
	}
	return true
}

func Origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func (o Options) private(u *url.URL) bool { return matchesOrigin(o.PrivateOrigins, u) }

func matchesOrigin(origins []string, u *url.URL) bool {
	for _, raw := range origins {
		v, err := url.Parse(raw)
		if err == nil && Origin(v) == Origin(u) {
			return true
		}
	}
	return false
}

func (o Options) validate(u *url.URL) error {
	if u == nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(u.Host, "%\\") || (u.Scheme != "https" && !(u.Scheme == "http" && matchesOrigin(o.HTTPOrigins, u))) {
		return ErrTarget
	}
	return nil
}

type transport struct{ options Options }

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.options.validate(req.URL); err != nil {
		return nil, err
	}
	private := t.options.private(req.URL)
	tr := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10,
	}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialValidated(ctx, network, address, private, net.DefaultResolver.LookupNetIP, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
	}

	return tr.RoundTrip(req)
}

func New(o Options) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: transport{o}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || (via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https") {
			return ErrTarget
		}
		if Origin(req.URL) != Origin(via[len(via)-1].URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
		return o.validate(req.URL)
	}}
}

// ReadBounded detects overrun rather than successfully parsing a truncated body.
func ReadBounded(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrSize
	}
	return b, nil
}

// CopyBounded streams at most max bytes; an extra byte is checked without
// ever writing it to the destination. Callers discard partial output on error.
func CopyBounded(dst io.Writer, src io.Reader, max int64) (int64, error) {
	if max < 0 {
		return 0, ErrSize
	}
	n, err := io.CopyBuffer(dst, io.LimitReader(src, max), make([]byte, 32<<10))
	if err != nil {
		return n, err
	}
	var extra [1]byte
	k, err := io.ReadFull(src, extra[:])
	if k > 0 {
		return n, ErrSize
	}
	if err == io.EOF {
		return n, nil
	}
	return n, err
}

// Gate bounds concurrent work without an unbounded waiter queue.
type Gate struct {
	once  sync.Once
	ch    chan struct{}
	Limit int
}

func (g *Gate) Acquire() bool {
	g.once.Do(func() {
		n := g.Limit
		if n < 1 {
			n = 4
		}
		g.ch = make(chan struct{}, n)
	})
	select {
	case g.ch <- struct{}{}:
		return true
	default:
		return false
	}
}
func (g *Gate) Release() { <-g.ch }

func dialValidated(ctx context.Context, network, address string, private bool, resolve func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, ErrTarget
	}
	ips, e := resolve(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, ErrTarget
	}
	for _, ip := range ips {
		if !AllowedIP(ip, private) {
			return nil, ErrTarget
		}
	}
	for _, ip := range ips {
		var c net.Conn
		c, e = dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
	}
	return nil, e
}
