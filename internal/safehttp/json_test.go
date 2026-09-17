package safehttp

import (
	"strings"
	"testing"
)

func TestJSONAmplificationRejected(t *testing.T) {
	for _, raw := range []string{
		strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33),
		`{"v":"` + strings.Repeat("x", 64<<10+1) + `"}`,
		`[` + strings.Repeat("{},", 128<<10) + `{}]`,
	} {
		if err := CheckJSONBudget([]byte(raw)); err == nil {
			t.Fatal("unbounded structure accepted")
		}
	}
	if err := CheckJSONBudget([]byte(`{"nodes":[{"id":1}],"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
}
