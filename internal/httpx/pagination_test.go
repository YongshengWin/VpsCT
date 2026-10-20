package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestPaginationPreservesOffsetsAndIDsBeyondCountLimit(t *testing.T) {
	r := httptest.NewRequest("GET", "/?offset=10001&before=9223372036854775807&limit=10001", nil)
	if got := QueryOffset(r, "offset", 0); got != 10001 {
		t.Fatalf("pagination offset was truncated: %d", got)
	}
	if got := QueryInt64(r, "before", 0); got != 9223372036854775807 {
		t.Fatalf("ID cursor was truncated: %d", got)
	}
	if got := QueryInt(r, "limit", 100); got != 10000 {
		t.Fatalf("count cap was removed: %d", got)
	}
	for _, value := range []string{"", "-1", "invalid", "9223372036854775808"} {
		r := httptest.NewRequest("GET", "/?offset="+value+"&before="+value, nil)
		if got := QueryOffset(r, "offset", 7); got != 7 {
			t.Fatalf("invalid offset %q: %d", value, got)
		}
		if got := QueryInt64(r, "before", 7); got != 7 {
			t.Fatalf("invalid cursor %q: %d", value, got)
		}
	}
}
