package proxysandbox

import (
	"slices"
	"testing"
)

func TestXrayFixedIdentity(t *testing.T) {
	for action, want := range map[string][]string{
		"run":       {"run", "-config", "/etc/ctlvps-proxy/xray-443/config.json"},
		"candidate": {"run", "-test", "-config", "/etc/ctlvps-proxy/xray-443/candidate.json"},
	} {
		bin, args, err := Command([]string{"xray", action, "443"})
		if err != nil || bin != "/opt/ctlvps/bin/xray" || !slices.Equal(args, want) {
			t.Fatalf("%s: %s %q %v", action, bin, args, err)
		}
	}
	for _, args := range [][]string{{"xray", "run", "0"}, {"xray", "run", "0443"}, {"xray", "candidate", "65536"}, {"xray", "run", "+443"}, {"xray", "candidate", "../../etc"}, {"xray", "test", "443"}, {"xray", "run", "443", "-config"}} {
		if _, _, err := Command(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestFixedCommandBoundary(t *testing.T) {
	for _, args := range [][]string{{"singbox", "run", "public"}, {"singbox", "candidate", "private"}, {"snell", "run", "443"}} {
		if _, _, e := Command(args); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{"singbox", "run", "../../etc"}, {"singbox", "run", "/etc/passwd"}, {"snell", "run", "0443"}, {"snell", "run", "0"}, {"snell", "run", "65536"}, {"singbox", "shell", "public"}, {"sh", "run", "public"}, {"singbox", "run", "public", "-c", "/tmp/evil"}} {
		if _, _, e := Command(args); e == nil {
			t.Fatalf("accepted arbitrary command: %q", args)
		}
	}
}
