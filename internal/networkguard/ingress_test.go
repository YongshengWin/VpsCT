package networkguard

import (
	"strings"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestRulesIncludeRelayOnlyIngressForBothFamilies(t *testing.T) {
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", Ingress: &networkconfig.IngressPolicy{Mode: networkconfig.IngressRelayOnly, SourceCIDRs: []string{"203.0.113.10/32", "2001:db8::/64"}}}
	p, err := New([]Binding{{NodeID: 7, ListenPort: 24443, Core: "singbox", Pending: true, Wanted: policy}})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := p.Rules()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"n7_src4", "n7_src6", "ip saddr != @n7_src4 drop", "ip6 saddr != @n7_src6 drop", "ip daddr != @n7_src4 drop", "ip6 daddr != @n7_src6 drop"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("rules missing %q:\n%s", want, rules)
		}
	}
}

func TestRulesDenyGeoAllowsExplicitRelayException(t *testing.T) {
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", Ingress: &networkconfig.IngressPolicy{Mode: networkconfig.IngressDenyCountries, Countries: []string{"CN"}, GeoProvider: networkconfig.GeoIPProviderDBIP, GeoVersion: "2026-11", SourceCIDRs: []string{"203.0.113.10/32", "2001:db8::/128"}, GeoIPv4CIDRs: []string{"1.1.1.0/24"}, GeoIPv6CIDRs: []string{"2001:db8:1::/48"}}}
	p, err := New([]Binding{{NodeID: 8, ListenPort: 24444, Core: "singbox", Pending: true, Wanted: policy}})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := p.Rules()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rules, "n8_geo4") || !strings.Contains(rules, "ip saddr != { 203.0.113.10/32 } ip saddr @n8_geo4 drop") {
		t.Fatalf("missing IPv4 geo deny/exception rule:\n%s", rules)
	}
	if !strings.Contains(rules, "n8_geo6") || !strings.Contains(rules, "ip6 saddr != { 2001:db8::/128 } ip6 saddr @n8_geo6 drop") {
		t.Fatalf("missing IPv6 geo deny/exception rule:\n%s", rules)
	}
}
