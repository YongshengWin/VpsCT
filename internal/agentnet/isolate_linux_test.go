//go:build linux

package agentnet

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestIsolatedChild(t *testing.T) {
	if os.Getenv("CTLVPS_NETWORK_CHILD") != "1" {
		return
	}
	if os.Geteuid() == 0 {
		os.Exit(3)
	}
	groups, e := os.Getgroups()
	if e != nil || len(groups) != 0 {
		os.Exit(4)
	}
	if e = harden(); e != nil {
		os.Exit(5)
	}
	if f, e := os.Open("/fixtures/private/secret"); e == nil {
		f.Close()
		os.Exit(6)
	}
	if f, e := os.OpenFile("/fixtures/private/secret", os.O_WRONLY, 0); e == nil {
		f.Close()
		os.Exit(7)
	}
	b, e := os.ReadFile("/proc/self/status")
	if e != nil {
		os.Exit(8)
	}
	for _, s := range []string{"NoNewPrivs:\t1", "CapEff:\t0000000000000000"} {
		if !strings.Contains(string(b), s) {
			os.Exit(9)
		}
	}
	json.NewEncoder(os.Stdout).Encode(map[string]int{"uid": os.Getuid(), "gid": os.Getgid()})
	os.Exit(0)
}
func TestDedicatedNetworkIdentityContainer(t *testing.T) {
	if os.Getenv("CTLVPS_ISOLATION_TEST") != "1" {
		t.Skip("explicit disposable container required")
	}
	if _, e := os.Stat("/.dockerenv"); e != nil || os.Geteuid() != 0 {
		t.Fatal("container root required")
	}
	os.MkdirAll("/fixtures/private", 0700)
	os.WriteFile("/fixtures/private/secret", []byte("synthetic"), 0600)
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolatedChild$")
	cmd.Env = []string{"CTLVPS_NETWORK_CHILD=1"}
	if e := isolate(cmd); e != nil {
		t.Fatal(e)
	}
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("isolated child failed: %v %s", e, raw)
	}
	var got map[string]int
	if e = json.Unmarshal(raw, &got); e != nil {
		t.Fatal(e)
	}
	if got["uid"] < 100 || got["uid"] == 65534 || got["gid"] == 0 {
		t.Fatal("invalid child identity", got)
	}
	// A pre-existing interactive/shared identity must not be accepted on retry.
	if e = exec.Command("/usr/sbin/usermod", "--shell", "/bin/sh", "ctlvps-net").Run(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = networkIdentity(); e == nil {
		t.Fatal("interactive account reused")
	}
	if e = exec.Command("/usr/sbin/usermod", "--shell", "/usr/sbin/nologin", "ctlvps-net").Run(); e != nil {
		t.Fatal(e)
	}
	if e = exec.Command("/usr/bin/gpasswd", "--add", "nobody", "ctlvps-net").Run(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = networkIdentity(); e == nil {
		t.Fatal("shared group accepted")
	}
}
