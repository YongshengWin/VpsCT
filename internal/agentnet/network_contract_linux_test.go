//go:build linux

package agentnet

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"ctlvps/internal/networkconfig"
)

// Opt-in because the actual dedicated unprivileged helper needs a fixture CA
// and a system identity. The runner guarantees a disposable offline container.
func TestNetworkContractContainer(t *testing.T) {
	if os.Getenv("CTLVPS_NETWORK_CONTRACT_TEST") != "1" {
		t.Skip("disposable container required")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil || os.Geteuid() != 0 {
		t.Fatal("container root required")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Unrelated-Header") != "" {
			t.Error("arbitrary header escaped the isolated transport allowlist")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": r.Header.Get(networkconfig.BindingHeader), "egress": r.Header.Get(networkconfig.EgressHeader), "ssh": r.Header.Get(networkconfig.SSHHeader)})
	}))
	defer srv.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile("/usr/local/share/ca-certificates/ctlvps-network-contract-fixture.crt", cert, 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("update-ca-certificates").CombinedOutput(); err != nil {
		t.Fatalf("fixture CA: %v %s", err, out)
	}
	for _, tc := range []struct {
		method, path, version, want, egress, wantEgress string
		public                                          bool
	}{
		{"GET", "/api/agent/v1/desired", "1", "1", "1", "1", false},
		{"GET", "/api/agent/v1/desired", "1", "1", "", "", false},
		{"GET", "/api/agent/v1/desired", "", "", "", "", false},
		{"POST", "/api/agent/v1/heartbeat", "1", "", "1", "", false},
		{"GET", "/dl/fixture", "1", "", "1", "", true},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		req.Header.Set(networkconfig.BindingHeader, tc.version)
		req.Header.Set(networkconfig.EgressHeader, tc.egress)
		req.Header.Set(networkconfig.SSHHeader, tc.egress)
		req.Header.Set("X-Unrelated-Header", "must-not-forward")
		resp, err := (Transport{Public: tc.public, PrivateOrigin: tc.public}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		err = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if err != nil || got["version"] != tc.want || got["egress"] != tc.wantEgress || got["ssh"] != tc.wantEgress {
			t.Fatal("isolated worker lost or invented parent capability", got, tc, err)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/agent/v1/desired", nil)
	req.Header.Set(networkconfig.BindingHeader, "2")
	if _, err := (Transport{}).RoundTrip(req); err == nil {
		t.Fatal("unknown protocol version accepted")
	}
	req.Header.Set(networkconfig.BindingHeader, "1")
	req.Header.Set(networkconfig.EgressHeader, "2")
	if _, err := (Transport{}).RoundTrip(req); err == nil {
		t.Fatal("unknown egress version accepted")
	}
	req.Header.Del(networkconfig.BindingHeader)
	req.Header.Set(networkconfig.EgressHeader, "1")
	if _, err := (Transport{}).RoundTrip(req); err == nil {
		t.Fatal("egress capability accepted without binding capability")
	}
}
