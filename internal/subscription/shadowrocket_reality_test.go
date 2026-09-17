package subscription

import (
	"strings"
	"testing"
)

func TestShadowrocketNativeRealityParameters(t *testing.T) {
	rendered, err := RenderShadowrocket(sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	body := string(rendered.Body)
	for _, want := range []string{"pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "sid=ab", "fp=chrome", "peer=www.apple.com", "flow=xtls-rprx-vision"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing native Shadowrocket field %s", want)
		}
	}
	for _, bad := range []string{"public-key=", "short-id=", "client-fingerprint="} {
		if strings.Contains(body, bad) {
			t.Fatalf("YAML field emitted as native option %s", bad)
		}
	}
}
