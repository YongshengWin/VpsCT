package networkconfig

import (
	"errors"
	"net/netip"
	"strings"

	"ctlvps/internal/safehttp"
)

var globalIPv6 = netip.MustParsePrefix("2000::/3")

func IsPublicIPv6(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	return err == nil && globalIPv6.Contains(ip) && safehttp.AllowedIP(ip, false)
}

// NormalizeIPv6Host validates the reference. Ownership and DNS are checked
// against the target's current network inventory before deployment.
func NormalizeIPv6Host(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if err := ValidateAdvertiseHost(host); err != nil {
		return "", err
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !IsPublicIPv6(host) {
			return "", errors.New("访问地址须使用公网 IPv6")
		}
		return ip.String(), nil
	}
	if !strings.Contains(host, ".") {
		return "", errors.New("请填写公开 DNS 域名或公网 IPv6")
	}
	return host, nil
}
