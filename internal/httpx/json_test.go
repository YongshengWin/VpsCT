package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeAmbiguity(t *testing.T) {
	for _, s := range []string{`{"role":"user","Role":"admin"}`, `{} {}`, `{"a":{"x":1,"x":2}}`, strings.Repeat("[", 70) + strings.Repeat("]", 70), strings.Repeat(" ", 4<<20) + "{}"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(s))
		var v any
		if Decode(r, &v) == nil {
			t.Fatal("ambiguous or oversized JSON accepted")
		}
	}
}
