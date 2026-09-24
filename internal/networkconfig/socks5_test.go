package networkconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSOCKS5SeparatesBusinessAndOuterFamilies(t *testing.T) {
	cfg := SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "password", UDP: true, Family: "ipv6", DNS: Resolver{Transport: "udp", Address: "2001:db8::53", Port: 53},
		Outer: Direct{Family: "ipv4", DNS: Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}, ConnectTimeoutSeconds: 10}
	if err := cfg.Validate(); err != nil {
		t.Fatal("IPv6 business can travel through an IPv4 SOCKS endpoint", err)
	}
	cfg.UDP = false
	if err := cfg.Validate(); err == nil {
		t.Fatal("UDP DNS cannot bypass a TCP-only SOCKS transport")
	}
	cfg.DNS.Transport = "tcp"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Server = "2001:db8::1"
	if err := cfg.Validate(); err == nil {
		t.Fatal("IPv6 outer endpoint accepted with IPv4-only outer transport")
	}
}

func TestSOCKS5PublicConfigurationRejectsCredentialsAndArbitraryCoreFields(t *testing.T) {
	cfg := SOCKS5{Server: "UPSTREAM.Example.", ServerPort: 1080, Authentication: "none", UDP: false, Family: "ipv4", DNS: Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53},
		Outer: Direct{Family: "ipv4", DNS: Resolver{Transport: "tcp", Address: "192.0.2.54", Port: 53}}, ConnectTimeoutSeconds: 10}
	b, _ := json.Marshal(cfg)
	parsed, err := DecodeSOCKS5(b)
	if err != nil || parsed.Server != "upstream.example" {
		t.Fatal(parsed, err)
	}
	for _, field := range []string{"password", "username", "detour", "routing_mark", "udp_over_tcp"} {
		bad := append([]byte(nil), b[:len(b)-1]...)
		bad = append(bad, []byte(`,"`+field+`":"private-test-value"}`)...)
		if _, err := DecodeSOCKS5(bad); err == nil || strings.Contains(err.Error(), "private-test-value") {
			t.Fatal("unsafe public profile or error", field, err)
		}
	}
	if err := (SOCKS5Credentials{Username: "fixture", Password: "fixture"}).Validate("none"); err == nil {
		t.Fatal("no-auth profile accepted credentials")
	}
	if err := (SOCKS5Credentials{Username: "fixture", Password: strings.Repeat("x", 256)}).Validate("password"); err == nil {
		t.Fatal("RFC1929 oversized credential accepted")
	}
}
