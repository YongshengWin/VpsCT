package core

import (
	"crypto/ecdh"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVLESSRealityUsesPinnedHandshakeThroughTransport(t *testing.T) {
	dns := networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}
	outer := networkconfig.Direct{Family: "ipv4", DNS: dns}
	cfg := networkconfig.SS2022{Server: "203.0.113.12", ServerPort: 8388, Method: "2022-blake3-aes-128-gcm", Family: "ipv4", DNS: dns, Outer: outer, ConnectTimeoutSeconds: 10}
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "fixture", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: strings.Repeat("1", 32), Generation: strings.Repeat("b", 32), Name: "wan0", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	resolved, err := netinventory.ResolveSOCKS5(policy, cfg.Transport(), snapshot, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n := specFor(t, domain.Server{ID: 1, PublicHost: "entry.example.test", CoreMode: domain.CoreModeStable}, 1, "vless", 24443, false)
	n.Params["handshake_server"] = "handshake.example.test"
	n.Network = &agentproto.NodeNetworkSpec{Policy: policy, SS2022: &agentproto.SS2022Egress{Config: cfg, Credentials: networkconfig.SOCKS5Credentials{Password: "AAAAAAAAAAAAAAAAAAAAAA=="}}}
	n.RuntimeNetwork = &resolved
	ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: corecompat.NetworkBaseline}}}
	driver := &SingBox{}
	if _, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("unverified handshake admitted")
	}
	resolved.Handshake = &networkconfig.ResolvedHandshake{Host: "handshake.example.test", Address: "203.0.113.25", Port: 443}
	compiled, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n})
	if err != nil {
		t.Fatal(err)
	}
	inbound := compiled["inbounds"].([]any)[0].(map[string]any)
	tls := inbound["tls"].(map[string]any)
	handshake := tls["reality"].(map[string]any)["handshake"].(map[string]any)
	if handshake["server"] != "203.0.113.25" || handshake["detour"] != "node-1-socks5" || tls["server_name"] != "handshake.example.test" {
		t.Fatal("handshake escaped the validated proxy pin", handshake)
	}
	if compiled["outbounds"].([]any)[0].(map[string]any)["type"] != "shadowsocks" {
		t.Fatal("wrong transport")
	}
	if path := os.Getenv("CTLVPS_PROXY_CHECK_CONFIG_PATH"); path != "" {
		keyBytes, err := base64.RawURLEncoding.DecodeString(n.Params["reality_private_key"].(string))
		if err != nil {
			t.Fatal(err)
		}
		key, err := ecdh.X25519().NewPrivateKey(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".public-key", []byte(base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())), 0600); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(compiled)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Real official binary validates the complete production JSON, including Reality.
	if bin := os.Getenv("CTLVPS_OFFICIAL_CHECK_BIN"); bin != "" {
		path := filepath.Join(t.TempDir(), "vless.json")
		body, _ := json.Marshal(compiled)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "check", "-c", path).CombinedOutput(); err != nil {
			t.Fatalf("official check: %v %s", err, out)
		}
	}
	for _, address := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "::1"} {
		resolved.Handshake.Address = address
		if _, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
			t.Fatal("private handshake escaped through proxy", address)
		}
	}
	resolved.Handshake.Address = "203.0.113.25"
	resolved.Handshake.Host = "other.example.test"
	if _, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("a pin from another hostname was reused")
	}
}
