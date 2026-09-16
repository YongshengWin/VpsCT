package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestNativeACMEStateMount(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_SYSTEMD_TEST") != "1" {
		t.Skip("disposable systemd container only")
	}
	ctx := context.Background()
	sd := NewSystemd()
	uid, gid, e := proxyIdentity("ctlvps-sb")
	if e != nil {
		t.Fatal(e)
	}
	source := "/var/lib/ctlvps-agent/acme"
	target := "/var/lib/ctlvps-proxy/public/acme"
	if e = os.MkdirAll(source, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Dir(source), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(source, "account-fixture"), []byte("retained-account"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = prepareACME(source, int(uid), int(gid)); e != nil {
		t.Fatal(e)
	}
	if e = secureDir(target, 0, 0, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e = sd.EnsureSingBoxSlice(ctx, false); e != nil {
		t.Fatal(e)
	}
	script := "/opt/acme-mount-fixture.py"
	if e = os.WriteFile(script, []byte("from pathlib import Path\np=Path('/var/lib/ctlvps-proxy/public/acme/account-fixture')\nassert p.read_text()=='retained-account'\np.write_text('renewal-state-fixture')\ntry:\n Path('/var/lib/ctlvps-agent/acme/account-fixture').read_text()\n raise AssertionError('management parent accessible')\nexcept PermissionError:pass\n"), 0644); e != nil {
		t.Fatal(e)
	}
	props := proxyProperties("ctlvps-sb", "/etc/ctlvps-proxy/public", "-/var/log/ctlvps/sing-box.log", SingBoxSlice(false), source, target, true)
	props = append(props, "Type=oneshot", "Restart=no")
	name := "ctlvps-acme-fixture.service"
	_, e = sd.WriteUnit(name, ServiceUnit("ACME storage fixture", "/usr/bin/python3 "+script, agentproto.Tuning{}, props...))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = sd.StopUnits(ctx, []string{name})
		_ = os.Remove(filepath.Join(sd.UnitDir, name))
		_ = sd.DaemonReload(ctx)
	})
	if e = sd.DaemonReload(ctx); e != nil {
		t.Fatal(e)
	}
	if e = sd.StartUnits(ctx, []string{name}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(source, "account-fixture"))
	if e != nil || string(b) != "renewal-state-fixture" {
		t.Fatal("ACME state not preserved", e)
	}
}
