package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestKernelProxyPreflightDiagnostics(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_SYSTEMD_TEST") != "1" {
		t.Skip("disposable systemd container only")
	}
	ctx := context.Background()
	sd := NewSystemd()
	launcher := "/usr/local/bin/ctlvps-issue-preflight"
	t.Cleanup(func() { _ = os.Remove(launcher) })
	for _, tc := range []struct{ output, code string }{
		{"[proxy_mark_unavailable] fixture-private-config", "proxy_mark_unavailable"},
		{"unrelated current failure fixture-private-config", "proxy_preflight_failed"},
		{"[proxy_sandbox_unavailable] fixture-private-config", "proxy_sandbox_unavailable"},
	} {
		if err := os.MkdirAll(filepath.Dir(launcher), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(launcher, []byte("#!/bin/sh\necho '"+tc.output+"' >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		err := sd.CheckProxy(ctx, launcher, "public", nil)
		if err == nil || !strings.Contains(err.Error(), "["+tc.code+"]") || strings.Contains(err.Error(), "fixture-private-config") {
			t.Fatalf("wrong current invocation diagnosis: %v want %s", err, tc.code)
		}
	}
}

func TestKernelProxySliceValidation(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_SYSTEMD_TEST") != "1" {
		t.Skip("disposable systemd container only")
	}
	ctx := context.Background()
	sd := NewSystemd()
	if _, err := sd.EnsureSingBoxSlice(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sd.EnsureSingBoxSlice(ctx, true); err != nil {
		t.Fatal(err)
	}
	unit := "ctlvps-issue-slice.service"
	body := ServiceUnit("slice fixture", "/bin/sleep 60", agentproto.Tuning{}, "Slice="+SingBoxSlice(false))
	if _, err := sd.WriteUnit(unit, body); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sd.StopUnits(ctx, []string{unit})
		_ = os.Remove(sd.UnitDir + "/" + unit)
		_ = sd.DaemonReload(ctx)
	}()
	if err := sd.DaemonReload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sd.StartUnits(ctx, []string{unit}); err != nil {
		t.Fatal(err)
	}
	if !sd.InSlice(ctx, unit, SingBoxSlice(false)) {
		t.Fatal("actual service leaf rejected")
	}
	if sd.InSlice(ctx, unit, SingBoxSlice(true)) || sd.InSlice(ctx, unit, "ctlvps-proxy.slice") {
		t.Fatal("wrong leaf or parent accepted")
	}
}

func TestServiceUnitHasOneEffectiveSlice(t *testing.T) {
	for _, extras := range [][]string{nil, {"Slice=ctlvps-proxy-public.slice"}, {"Slice=ctlvps-proxy-public.slice", "Slice=system.slice"}} {
		body := ServiceUnit("fixture", "/bin/true", agentproto.Tuning{}, extras...)
		if strings.Count(body, "\nSlice=") != 1 {
			t.Fatalf("ambiguous Slice:\n%s", body)
		}
		want := "ctlvps-proxy.slice"
		if len(extras) > 0 {
			want = strings.TrimPrefix(extras[len(extras)-1], "Slice=")
		}
		if !strings.Contains(body, "\nSlice="+want+"\n") {
			t.Fatal(body)
		}
	}
}

func TestCgroupProofRequiresExactLeaf(t *testing.T) {
	parent := "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice"
	unit := "ctlvps-singbox.service"
	for _, group := range []string{parent, parent + "-other/" + unit, parent + "/../" + unit, parent + "/child/" + unit, parent + "/" + unit + "-other", "", "/system.slice/" + unit} {
		if directCgroupChild(group, parent, unit) {
			t.Fatalf("accepted %q", group)
		}
	}
	if !directCgroupChild(parent+"/"+unit, parent, unit) {
		t.Fatal("valid leaf rejected")
	}
	if directCgroupChild("/"+unit, "/", unit) {
		t.Fatal("root accepted as protected slice")
	}
}

func TestProxyPrivilegesRemainMinimal(t *testing.T) {
	for _, marked := range []bool{false, true} {
		props := proxyProperties("ctlvps-sb", "/etc/ctlvps-proxy/public", "/var/log/ctlvps/sing-box.log", SingBoxSlice(false), "", "", marked)
		body := ServiceUnit("fixture", "/bin/true", agentproto.Tuning{}, props...)
		want := "CAP_NET_BIND_SERVICE"
		if marked {
			want += " CAP_NET_RAW"
		}
		for _, key := range []string{"CapabilityBoundingSet", "AmbientCapabilities"} {
			if !strings.Contains(body, "\n"+key+"=\n"+key+"="+want+"\n") {
				t.Fatal("capability reset or minimal policy missing", body)
			}
		}
	}
}
