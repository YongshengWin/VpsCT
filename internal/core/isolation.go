package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"ctlvps/internal/agentproto"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Installer-managed /etc/ctlvps may be 0750 and must stay private. Give proxy
// runtime configurations their own traversable parent instead of relaxing it.
const proxyConfigRoot = "/etc/ctlvps-proxy"
const proxyStateRoot = "/var/lib/ctlvps-proxy"

func SingBoxSlice(private bool) string {
	if private {
		return "ctlvps-proxy-private.slice"
	}
	return "ctlvps-proxy-public.slice"
}

func (s *Systemd) EnsureSingBoxSlice(ctx context.Context, private bool) (string, error) {
	name := SingBoxSlice(private)
	c, e := s.WriteUnit(name, "[Unit]\nDescription=VpsCT proxy permission group\n[Slice]\n")
	if e != nil {
		return "", e
	}
	if c {
		if e = s.DaemonReload(ctx); e != nil {
			return "", e
		}
	}
	if e = s.StartUnits(ctx, []string{name}); e != nil {
		return "", e
	}
	return s.ControlGroup(ctx, name)
}

// secureDir refuses links and writable parents before any ownership change.
func secureDir(path string, uid, gid int, mode fs.FileMode) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == "/" {
		return fmt.Errorf("invalid proxy directory")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	cur := "/"
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		if e := os.Mkdir(cur, 0755); e != nil && !os.IsExist(e) {
			return e
		}
		st, e := os.Lstat(cur)
		if e != nil {
			return e
		}
		if stat, ok := st.Sys().(*syscall.Stat_t); !ok || (i < len(parts)-1 && stat.Uid != 0) {
			return fmt.Errorf("untrusted proxy parent %s", cur)
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe proxy directory %s", cur)
		}
		if i < len(parts)-1 && st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("writable proxy parent %s", cur)
		}
	}
	if e := os.Chown(path, uid, gid); e != nil {
		return e
	}
	return os.Chmod(path, mode)
}

func proxyFile(path string, data []byte, gid int) (bool, error) {
	c, e := WriteIfChanged(path, data, 0640)
	if e != nil {
		return false, e
	}
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() {
		return false, fmt.Errorf("invalid runtime file")
	}
	if e = os.Chown(path, 0, gid); e != nil {
		return false, e
	}
	return c, os.Chmod(path, 0640)
}

func snapshotCert(src CertFiles, dir string, gid int) (CertFiles, error) {
	read := func(path string) ([]byte, error) {
		st, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !st.Mode().IsRegular() || st.Size() > 4<<20 {
			return nil, fmt.Errorf("invalid certificate source")
		}
		return os.ReadFile(path)
	}
	cert, e := read(src.Cert)
	if e != nil {
		return CertFiles{}, e
	}
	key, e := read(src.Key)
	if e != nil {
		return CertFiles{}, e
	}
	pair, e := tls.X509KeyPair(cert, key)
	if e != nil {
		return CertFiles{}, fmt.Errorf("certificate/key mismatch: %w", e)
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return CertFiles{}, e
	}
	if time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return CertFiles{}, fmt.Errorf("certificate outside validity period")
	}
	h := sha256.Sum256(append(append([]byte{}, cert...), key...))
	base := filepath.Join(dir, fmt.Sprintf("tls-%x", h[:16]))
	f := CertFiles{Cert: base + ".crt", Key: base + ".key"}
	if _, e = proxyFile(f.Cert, cert, gid); e != nil {
		return f, e
	}
	_, e = proxyFile(f.Key, key, gid)
	return f, e
}

// Give only native ACME state to this proxy. The management parent stays root
// private; systemd binds this subtree at a separate visible runtime location.
func prepareACME(source string, uid, gid int) error {
	if e := os.MkdirAll(source, 0700); e != nil {
		return e
	}
	st, e := os.Lstat(source)
	if e != nil {
		return e
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return fmt.Errorf("ACME state already owned by a proxy; explicit offline ownership migration required")
	}
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("ACME state contains a symlink")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("invalid ACME state entry")
		}
		if e = os.Chown(path, uid, gid); e != nil {
			return e
		}
		mode := fs.FileMode(0600)
		if d.IsDir() {
			mode = 0700
		}
		return os.Chmod(path, mode)
	})
}

func proxyProperties(user, dir, logPath, slice, acmeSource, acmeTarget string, marked bool) []string {
	caps := "CAP_NET_BIND_SERVICE"
	if marked {
		caps += " CAP_NET_RAW"
	}
	p := []string{"User=" + user, "Group=" + user, "SupplementaryGroups=", "Slice=" + slice, "CapabilityBoundingSet=", "CapabilityBoundingSet=" + caps, "AmbientCapabilities=", "AmbientCapabilities=" + caps, "ReadWritePaths=", "ReadWritePaths=" + logPath, "InaccessiblePaths=-/var/lib/ctlvps-agent -/var/lib/ctlvps-security -/var/lib/ctlvps-maintenance -/run/ctlvps-maintenance", "UMask=0077", "RestrictNamespaces=true", "ProtectKernelModules=true", "ProtectKernelLogs=true", "LockPersonality=true"}
	if acmeSource != "" {
		p = append(p, "BindPaths="+acmeSource+":"+acmeTarget, "ReadWritePaths="+acmeTarget)
	}
	return p
}

func proxyLauncher() (string, error) {
	const p = "/usr/local/bin/ctlvps-agent"
	st, e := os.Stat(p)
	if e != nil {
		return "", e
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("unsafe proxy launcher")
	}
	return p, nil
}

// Validate with exactly the service identity, mounts and capabilities before
// stopping a working process. A oneshot unit waits for the check exit status.
func (s *Systemd) CheckProxy(ctx context.Context, launcher, profile string, props []string) error {
	name := "ctlvps-proxy-check-" + profile + ".service"
	if profile != "public" && profile != "private" {
		return fmt.Errorf("invalid check profile")
	}
	props = append(append([]string{}, props...), "Type=oneshot", "Restart=no", "Slice=system.slice", "PrivateNetwork=true", "OOMScoreAdjust=1000")
	body := ServiceUnit("VpsCT isolated proxy preflight", launcher+" proxy-exec singbox candidate "+profile, agentproto.Tuning{}, props...)
	if _, e := s.WriteUnit(name, body); e != nil {
		return e
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.StopUnits(c, []string{name})
		_ = os.Remove(filepath.Join(s.UnitDir, name))
		_ = s.DaemonReload(c)
	}()
	if e := s.DaemonReload(ctx); e != nil {
		return e
	}
	if e := s.StartUnits(ctx, []string{name}); e != nil {
		return fmt.Errorf("isolated proxy preflight failed (see journal for %s): %w", name, e)
	}
	return nil
}

func snellProfile(port int) string { return "snell-" + strconv.Itoa(port) }

// Verify the running kernel identity, not just the generated unit text.
func (s *Systemd) CheckRunningProxy(ctx context.Context, unit, account, slice, command string, marked bool) error {
	identity, e := user.Lookup(account)
	if e != nil {
		return e
	}
	pid, e := s.ctl(ctx, "show", unit, "--property=MainPID", "--value")
	if e != nil {
		return e
	}
	id, e := strconv.Atoi(strings.TrimSpace(pid))
	if e != nil || id <= 0 {
		return fmt.Errorf("proxy has no running process")
	}
	raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", id))
	if e != nil {
		return e
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			fields[k] = strings.TrimSpace(v)
		}
	}
	uids := strings.Fields(fields["Uid"])
	if len(uids) != 4 {
		return fmt.Errorf("proxy identity unavailable")
	}
	for _, uid := range uids {
		if uid != identity.Uid {
			return fmt.Errorf("proxy identity differs from required account")
		}
	}
	allowed := uint64(1 << 10)
	if marked {
		allowed |= 1 << 13
	}
	for _, key := range []string{"CapEff", "CapPrm", "CapBnd", "CapAmb"} {
		v, e := strconv.ParseUint(fields[key], 16, 64)
		if e != nil || v&^allowed != 0 {
			return fmt.Errorf("proxy has unexpected capabilities")
		}
	}
	if fields["NoNewPrivs"] != "1" || fields["Seccomp"] != "2" {
		return fmt.Errorf("proxy sandbox is not active")
	}
	if !s.InSlice(ctx, unit, slice) {
		return fmt.Errorf("proxy is outside its protected slice")
	}
	execStart, e := s.ctl(ctx, "show", unit, "--property=ExecStart", "--value")
	if e != nil {
		return e
	}
	if !strings.Contains(execStart, "argv[]="+command+" ;") {
		return fmt.Errorf("proxy launcher was overridden")
	}
	return nil
}
