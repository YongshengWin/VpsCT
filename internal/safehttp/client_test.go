package safehttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIPPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.1.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "fc00::1", "64:ff9b::a00:1", "2002:7f00:1::", "100.64.0.1", "0.0.0.0", "224.0.0.1"} {
		if AllowedIP(netip.MustParseAddr(raw), false) {
			t.Fatalf("accepted %s", raw)
		}
	}
	if !AllowedIP(netip.MustParseAddr("8.8.8.8"), false) {
		t.Fatal("public rejected")
	}
	if AllowedIP(netip.MustParseAddr("169.254.169.254"), true) {
		t.Fatal("metadata exception")
	}
}
func TestPrivateOriginAndRedirectIsolation(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer target.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer entry.Close()
	for _, o := range []Options{{HTTPOrigins: []string{entry.URL}}, {HTTPOrigins: []string{entry.URL}, PrivateOrigins: []string{entry.URL}}} {
		resp, err := New(o).Get(entry.URL)
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil {
			t.Fatal("private redirect allowed")
		}
	}
	if hits.Load() != 0 {
		t.Fatal("blocked server was contacted")
	}
	resp, err := New(Options{HTTPOrigins: []string{target.URL}, PrivateOrigins: []string{target.URL}}).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}
func TestReadLimit(t *testing.T) {
	if _, e := ReadBounded(strings.NewReader("12345"), 4); e != ErrSize {
		t.Fatal(e)
	}
}

func TestDialPinsResolutionAndRejectsMixedAnswers(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		resolves, dials := 0, 0
		resolve := func(_ context.Context, _, _ string) ([]netip.Addr, error) {
			resolves++
			ips := []netip.Addr{netip.MustParseAddr("8.8.8.8")}
			if mixed || resolves > 1 {
				ips = append(ips, netip.MustParseAddr("127.0.0.1"))
			}
			return ips, nil
		}
		dial := func(_ context.Context, _, address string) (net.Conn, error) {
			dials++
			if address != "8.8.8.8:443" {
				t.Fatalf("not pinned: %s", address)
			}
			return nil, errors.New("synthetic network failure")
		}
		_, _ = dialValidated(context.Background(), "tcp", "rebind.example:443", false, resolve, dial)
		if resolves != 1 || (mixed && dials != 0) || (!mixed && dials != 1) {
			t.Fatalf("resolve=%d dial=%d mixed=%v", resolves, dials, mixed)
		}
	}
}
