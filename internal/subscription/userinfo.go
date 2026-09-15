// Package subscription imports external subscriptions and generates client
// configurations (mihomo / raw / Surge / Shadowrocket / sing-box).
package subscription

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/domain"
)

// shareUserinfo maps a share's counters onto subscription-userinfo.
// Clash/Surge used = upload+download, which is inbound+outbound.
func shareUserinfo(sh domain.Share) Userinfo {
	return Userinfo{Upload: sh.UsedUpload, Download: sh.UsedDownload, Total: sh.QuotaBytes, Expire: sh.ExpiresAt}
}

// Userinfo mirrors the subscription-userinfo header.
type Userinfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   *time.Time
}

// ParseUserinfo parses "upload=1; download=2; total=3; expire=1700000000".
func ParseUserinfo(h string) (Userinfo, bool) {
	var u Userinfo
	if strings.TrimSpace(h) == "" {
		return u, false
	}
	found := false
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			if f, ferr := strconv.ParseFloat(v, 64); ferr == nil {
				n = int64(f)
			} else {
				continue
			}
		}
		found = true
		switch k {
		case "upload":
			u.Upload = n
		case "download":
			u.Download = n
		case "total":
			u.Total = n
		case "expire":
			if n > 0 {
				t := time.Unix(n, 0).UTC()
				u.Expire = &t
			}
		}
	}
	return u, found
}

// Header renders the userinfo header value.
func (u Userinfo) Header() string {
	parts := []string{fmt.Sprintf("upload=%d", u.Upload), fmt.Sprintf("download=%d", u.Download)}
	if u.Total > 0 {
		parts = append(parts, fmt.Sprintf("total=%d", u.Total))
	}
	if u.Expire != nil {
		parts = append(parts, fmt.Sprintf("expire=%d", u.Expire.Unix()))
	}
	return strings.Join(parts, "; ")
}

// Remaining returns total - used (never negative), or -1 when unlimited.
func (u Userinfo) Remaining() int64 {
	if u.Total <= 0 {
		return -1
	}
	r := u.Total - u.Upload - u.Download
	if r < 0 {
		return 0
	}
	return r
}

// FormatBytes renders a human size (binary units, like most clients).
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// InfoLines builds the pseudo-node names shown at the top of a subscription.
func (u Userinfo) InfoLines(now time.Time) []string {
	var lines []string
	if u.Total > 0 {
		lines = append(lines, fmt.Sprintf("剩余流量：%s / %s", FormatBytes(u.Remaining()), FormatBytes(u.Total)))
	} else if u.Upload+u.Download > 0 {
		lines = append(lines, fmt.Sprintf("已用流量：%s", FormatBytes(u.Upload+u.Download)))
	}
	if u.Expire != nil {
		days := int(u.Expire.Sub(now).Hours() / 24)
		if days < 0 {
			lines = append(lines, "套餐已到期")
		} else {
			lines = append(lines, fmt.Sprintf("到期时间：%s（%d 天）", u.Expire.Format("2006-01-02"), days))
		}
	}
	return lines
}
