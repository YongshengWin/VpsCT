package agent

import (
	"strings"
	"time"
)

// Suppress repeated logging, never reconciliation or failure reports. External
// firewall/environment changes can repair the same desired revision at any poll.
type applyFailureLog struct {
	revision      int64
	hash, message string
	last          time.Time
}

func (f *applyFailureLog) matches(revision int64, hash string) bool {
	return !f.last.IsZero() && f.revision == revision && f.hash == hash
}

func (f *applyFailureLog) record(revision int64, hash, message string, now time.Time) bool {
	if f.matches(revision, hash) && f.message == message && now.Sub(f.last) >= 0 && now.Sub(f.last) < 5*time.Minute {
		return false
	}
	*f = applyFailureLog{revision: revision, hash: hash, message: message, last: now}
	return true
}

func applyFailureCode(message string) string {
	for _, code := range []string{"proxy_mark_unavailable", "proxy_sandbox_unavailable", "proxy_preflight_failed", "proxy_cgroup_mismatch", "ingress_policy_unsupported"} {
		if strings.Contains(message, "["+code+"]") {
			return code
		}
	}
	return "apply_failed"
}
