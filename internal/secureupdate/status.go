package secureupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TrustWarnings describes local cached freshness only, never claims the cache
// reflects an offline publisher's newest revocations. It does not stop services.
func TrustWarnings(dir string, now time.Time) []string {
	var warnings []string
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		raw, e := os.ReadFile(filepath.Join(dir, "metadata", role+".json"))
		if e != nil {
			continue
		}
		var envelope struct {
			Signed struct {
				Expires time.Time `json:"expires"`
			} `json:"signed"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Signed.Expires.IsZero() {
			warnings = append(warnings, "发布信任缓存损坏，需要本机检查")
			continue
		}
		if w := expiryWarning(role, envelope.Signed.Expires, now); w != "" {
			warnings = append(warnings, w)
		}
	}
	if p, e := readReleasePolicy(dir); e == nil {
		if w := expiryWarning("撤销策略", p.Expires, now); w != "" {
			warnings = append(warnings, w)
		}
	} else if !os.IsNotExist(e) {
		warnings = append(warnings, "撤销策略缓存损坏，需要本机检查")
	}
	return warnings
}
func expiryWarning(role string, expires, now time.Time) string {
	left := expires.Sub(now)
	if left <= 0 {
		return fmt.Sprintf("%s 已过期：新更新受阻，现有服务继续运行", role)
	}
	if left <= 24*time.Hour {
		return fmt.Sprintf("%s 将在 24 小时内过期，请续签发布元数据", role)
	}
	if left <= 72*time.Hour {
		return fmt.Sprintf("%s 将在 72 小时内过期，请续签发布元数据", role)
	}
	return ""
}
