package secureupdate

import (
	"strings"
	"testing"
	"time"
)

func TestExpiryWarnings(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{-time.Hour, "已过期"}, {12 * time.Hour, "24 小时"}, {48 * time.Hour, "72 小时"}, {96 * time.Hour, ""}} {
		got := expiryWarning("timestamp", now.Add(c.d), now)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Fatal(got, c.want)
		}
	}
}
