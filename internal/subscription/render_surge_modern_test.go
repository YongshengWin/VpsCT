package subscription

import (
	"errors"
	"strings"
	"testing"

	"ctlvps/internal/proxynode"
)

func TestModernSurgeSelectionAndTransport(t *testing.T) {
	p := sampleBundle().Proxies[0]
	p.Name = "vision"
	b := &Bundle{Proxies: []proxynode.Proxy{p}}
	requireUnavailable(t, b, FormatSurge)
	run := func() string {
		t.Helper()
		r, err := RenderBundleWithProfile(b, FormatSurge, ProfileSurgeVLESS)
		if err != nil || len(r.EmittedNames) != 1 {
			t.Fatalf("modern: %v", err)
		}
		return string(r.Body)
	}
	body := run()
	for _, want := range []string{"vision = vless,", "username=" + p.Str("uuid"), "reality-public-key=" + mapStr(p.Sub("reality-opts"), "public-key"), "reality-short-id=ab", "sni=www.apple.com", "client-fingerprint=chromium-155", "flow=xtls-rprx-vision", "FINAL,VpsCT Nodes"} {
		if !strings.Contains(body, want) {
			t.Fatalf("modern config missing %q", want)
		}
	}
	delete(p.Params, "flow")
	p.Params["network"] = "xhttp"
	p.Params["xhttp-opts"] = map[string]any{"path": "/transport", "host": "cover.example.test", "mode": "stream-one"}
	body = run()
	for _, want := range []string{"xhttp=true", "xhttp-path=/transport", "xhttp-host=cover.example.test", "xhttp-mode=stream-one", "reality-public-key="} {
		if !strings.Contains(body, want) {
			t.Fatalf("XHTTP lost %s", want)
		}
	}
	// A missing path or a standalone landing would appear to be a valid VLESS
	// line; assert the complete render is rejected, not just a helper returning "".
	p.Sub("xhttp-opts")["download-settings"] = map[string]any{"address": "elsewhere.example.test"}
	if r, e := RenderBundleWithProfile(b, FormatSurge, ProfileSurgeVLESS); r != nil || e == nil {
		t.Fatal("split download silently dropped", e)
	}
	delete(p.Sub("xhttp-opts"), "download-settings")
	p.Params["flow"] = "xtls-rprx-vision"
	if r, e := RenderBundleWithProfile(b, FormatSurge, ProfileSurgeVLESS); r != nil || !errors.Is(e, ErrNoRenderPath) {
		t.Fatal("XHTTP with Vision accepted", e)
	}
	for _, test := range []struct{ format, profile string }{{FormatSurge, "latest"}, {FormatSingBox, ProfileSurgeVLESS}} {
		if r, err := RenderBundleWithProfile(b, test.format, test.profile); r != nil || !errors.Is(err, ErrClientProfile) {
			t.Fatal("invalid profile accepted")
		}
	}
}
