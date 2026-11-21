package api

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestIPv6DeploymentStartsWithBindingAndWaitsForReceipt(t *testing.T) {
	c := setupAPIKeyTest(t)
	read, _ := issueTestAPIKey(t, c, 1, "read")
	write, _ := issueTestAPIKey(t, c, 1, "read", "write")
	const address = "2606:4700:100::12"
	c.api.ipv6Lookup = func(_ context.Context, family, host string) ([]netip.Addr, error) {
		if host == "timeout.example.net." {
			return nil, context.DeadlineExceeded
		}
		if family == "ip6" {
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}
		if host == "dual.example.net." {
			return []netip.Addr{netip.MustParseAddr("192.0.2.3")}, nil
		}
		return nil, &net.DNSError{IsNotFound: true}
	}
	keyRequest(t, c, "POST", "/api/v1/servers", write, map[string]any{
		"name": "invalid-draft", "public_host_ipv6": "dual.example.net",
	}, nil, 400)
	keyRequest(t, c, "POST", "/api/v1/servers", write, map[string]any{
		"name": "invalid-draft", "public_host_ipv6": "timeout.example.net",
	}, nil, 503)
	keyRequest(t, c, "POST", "/api/v1/servers", write, map[string]any{
		"name": "invalid-draft", "public_host_ipv6": address,
	}, nil, 201)
	server, _ := keyRequest(t, c, "POST", "/api/v1/servers", write, map[string]any{
		"name": "ipv6-fixture", "public_host_ipv6": "V6.Example.net.", "ipv4_only": true,
	}, nil, 201)
	if server["public_host_ipv6"] != "v6.example.net" || server["public_host"] != "" {
		t.Fatal("server did not keep independent IPv6 access")
	}
	base := fmt.Sprintf("/api/v1/servers/%.0f", server["id"])
	token := c.do("POST", base+"/enroll-token", nil, 200)
	agent := c.do("POST", agentproto.PathEnroll, agentproto.EnrollRequest{EnrollToken: token["token"].(string)}, 200)
	c.agent = agent["agent_token"].(string)
	iface := strings.Repeat("d", 32)
	hb := agentproto.Heartbeat{
		TS: time.Now().UTC(), Epoch: "boot",
		Diagnostics: agentproto.Diagnostics{NetworkBindingVersion: 1, IPv6IngressVersion: 1, NetworkConfigureAllowed: true,
			SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true},
		Metrics: agentproto.Metrics{Arch: "amd64", Network: &agentproto.NetworkSnapshot{
			Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok",
			Interfaces: []agentproto.NetworkInterface{{ID: iface, Generation: strings.Repeat("c", 32), Name: "wan0", Index: 2, Kind: "ether",
				Up: true, Carrier: true, Addresses: []string{address + "/64"}, UsableAddresses: []string{address + "/64"}}},
		}},
	}
	c.do("POST", agentproto.PathHeartbeat, hb, 200)
	keyRequest(t, c, "PUT", base, write, map[string]any{
		"name": "mismatch", "public_host_ipv6": "2606:4700:100::13",
	}, nil, 400)
	saved, _ := keyRequest(t, c, "GET", base, read, nil, nil, 200)
	if saved["name"] != "ipv6-fixture" || saved["public_host_ipv6"] != "v6.example.net" {
		t.Fatal("a failed IPv6 edit modified server settings")
	}
	body := map[string]any{
		"protocol": "anytls", "ingress_family": "ipv6",
		"network": networkconfig.Node{IngressFamily: "ipv6", ListenMode: "public_ipv6", AdvertiseMode: "inherit_ipv6", OnUnavailable: "block"},
	}
	c.api.ipv6Lookup = func(_ context.Context, family, _ string) ([]netip.Addr, error) {
		if family == "ip4" {
			return []netip.Addr{netip.MustParseAddr("192.0.2.3")}, nil
		}
		return []netip.Addr{netip.MustParseAddr(address)}, nil
	}
	endpoint := map[string]any{"host": "v6.example.net"}
	checkPath := base + "/ipv6-endpoint/check?host="
	keyRequest(t, c, "POST", base+"/ipv6-endpoint/check", read, endpoint, nil, 403)
	check, _ := keyRequest(t, c, "POST", base+"/ipv6-endpoint/check", write, endpoint, nil, 200)
	if check["dns_ready"] != false || check["matched"] != false {
		t.Fatal("accepted an IPv6 endpoint draft with an A record")
	}
	check, _ = keyRequest(t, c, "GET", checkPath+"v6.example.net", read, nil, nil, 200)
	if check["dns_ready"] != false || check["matched"] != false {
		t.Fatal("read-only check accepted an IPv6 endpoint with an A record")
	}
	unchanged, _ := keyRequest(t, c, "PUT", base, write, map[string]any{
		"name": "ipv6-fixture", "public_host_ipv6": "V6.EXAMPLE.NET.", "ipv4_only": true, "quota_bytes": 4096,
	}, nil, 200)
	if unchanged["quota_bytes"] != float64(4096) {
		t.Fatal("saving unrelated settings did not preserve normal behavior")
	}
	keyRequest(t, c, "GET", checkPath+url.QueryEscape("2001:db8::1"), read, nil, nil, 400)
	check, _ = keyRequest(t, c, "GET", checkPath+url.QueryEscape(strings.ToUpper(address)), read, nil, nil, 200)
	if check["matched"] != true || check["address"] != address {
		t.Fatal("failed to check a literal IPv6 without DNS")
	}
	literal := map[string]any{"protocol": "anytls", "ingress_family": "ipv6", "advertise_host": address,
		"network": networkconfig.Node{IngressFamily: "ipv6", ListenMode: "public_ipv6", AdvertiseMode: "override", OnUnavailable: "block"}}
	literalPreview, _ := keyRequest(t, c, "POST", base+"/nodes/preview", write, literal, nil, 200)
	if literalPreview["confirmation"] == nil || literalPreview["advertise_host"] != address {
		t.Fatal("literal IPv6 failed deployment preview")
	}
	keyRequest(t, c, "POST", base+"/nodes/preview", read, body, nil, 403)
	rejected, _ := keyRequest(t, c, "POST", base+"/nodes/preview", write, body, nil, 200)
	if rejected["confirmation"] != nil {
		t.Fatal("accepted a hostname with an A record")
	}
	c.api.ipv6Lookup = func(_ context.Context, family, _ string) ([]netip.Addr, error) {
		if family == "ip4" {
			return nil, &net.DNSError{IsNotFound: true}
		}
		return []netip.Addr{netip.MustParseAddr(address)}, nil
	}
	preview, _ := keyRequest(t, c, "POST", base+"/nodes/preview", write, body, nil, 200)
	check, _ = keyRequest(t, c, "POST", base+"/ipv6-endpoint/check", write, endpoint, nil, 200)
	if check["matched"] != true || check["address"] != address {
		t.Fatal("failed to match draft endpoint to the agent inventory")
	}
	body["operation_id"], body["expected_ipv6"], body["port"] = strings.Repeat("b", 32), preview["confirmation"], preview["port"]
	next, _ := keyRequest(t, c, "POST", base+"/nodes", write, body, nil, 201)
	retry, _ := keyRequest(t, c, "POST", base+"/nodes", write, body, nil, 201)
	if next["id"] != retry["id"] || next["server"] != "v6.example.net" {
		t.Fatal("deployment changed identity on retry or selected the wrong host")
	}
	nodePath := fmt.Sprintf("/api/v1/nodes/%.0f", next["id"])
	keyRequest(t, c, "GET", nodePath+"/uri", read, nil, nil, 409)
	keyRequest(t, c, "GET", agentproto.PathDesired, c.agent, nil, map[string]string{networkconfig.BindingHeader: "1"}, 409)
	state, _ := keyRequest(t, c, "GET", agentproto.PathDesired, c.agent, nil, map[string]string{
		networkconfig.BindingHeader: "1", networkconfig.IPv6IngressHeader: "1",
	}, 200)
	nodes := state["nodes"].([]any)
	n := nodes[0].(map[string]any)
	policy := n["network"].(map[string]any)["policy"].(map[string]any)
	if len(nodes) != 1 || policy["ingress_family"] != "ipv6" || policy["listen_interface_id"] != iface || state["ipv4_only"] != true {
		t.Fatal("first desired did not contain the constrained IPv6 listener and independent IPv4 egress")
	}
	report := agentproto.ApplyReport{Revision: int64(state["revision"].(float64)), Hash: state["hash"].(string), Status: "applied",
		NodeApplicationSequence: 1, NodeApplications: []agentproto.NodeApplication{{NodeID: int64(next["id"].(float64)), Core: string(domain.CoreSingBox), Status: "applied"}}}
	c.do("POST", agentproto.PathApplyReport, report, 400)
	report.NodeApplications[0].ListenIPv6 = address
	c.do("POST", agentproto.PathApplyReport, report, 204)
	node, _ := keyRequest(t, c, "GET", nodePath, read, nil, nil, 200)
	if node["ipv6_ingress_ready"] != true {
		t.Fatal("matching IPv6 receipt did not unlock the node")
	}
	keyRequest(t, c, "GET", nodePath+"/uri", read, nil, nil, 200)
	keyRequest(t, c, "PUT", base, write, map[string]any{"name": "ipv6-renamed", "public_host_ipv6": ""}, nil, 409)
	updated, _ := keyRequest(t, c, "PUT", base, write, map[string]any{"name": "ipv6-renamed", "ipv4_only": true}, nil, 200)
	if updated["public_host_ipv6"] != "v6.example.net" {
		t.Fatal("legacy update cleared the IPv6 host")
	}
	later := c.api.Store.Now().Add(2 * time.Minute)
	c.api.Store.Now = func() time.Time { return later }
	c.api.ipv6Lookup = func(_ context.Context, family, _ string) ([]netip.Addr, error) {
		if family == "ip4" {
			return nil, &net.DNSError{IsNotFound: true}
		}
		return []netip.Addr{netip.MustParseAddr("2606:4700:100::13")}, nil
	}
	if err := c.api.RefreshIPv6Endpoints(context.Background()); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "GET", nodePath+"/uri", read, nil, nil, 409)
}
