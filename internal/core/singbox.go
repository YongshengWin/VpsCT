package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ctlvps/internal/agentproto"
)

// SingBox drives a single sing-box service hosting all sing-box nodes.
type SingBox struct {
	Paths   Paths
	Systemd *Systemd
	// CertResolver returns cert/key paths for a node (self-signed/external).
	// It is injected so config generation is testable without disk access.
	CertResolver func(spec *agentproto.CertSpec) (CertFiles, error)
	// Email used for ACME registrations (optional).
	ACMEEmail string
}

// NewSingBox builds the driver with default resolvers.
func NewSingBox(p Paths, sd *Systemd) *SingBox {
	d := &SingBox{Paths: p, Systemd: sd}
	d.CertResolver = func(spec *agentproto.CertSpec) (CertFiles, error) {
		if spec.Mode == "external" {
			return CertFiles{Cert: spec.CertPath, Key: spec.KeyPath}, nil
		}
		return EnsureSelfSigned(p.CertDir, spec.Domain)
	}
	return d
}

const (
	singboxUnit         = "ctlvps-singbox.service" // legacy monolith, stopped on apply
	singboxTemplateUnit = "ctlvps-singbox@.service"
)

// SingBoxUnit is the per-listen-port instance (one cgroup, so IPAccounting
// sees every socket of that inbound — client and origin).
func SingBoxUnit(port int) string { return fmt.Sprintf("ctlvps-singbox@%d.service", port) }

// Name implements Driver.
func (d *SingBox) Name() string { return "singbox" }

func (d *SingBox) bin() string { return filepath.Join(d.Paths.BinDir, "sing-box") }

func (d *SingBox) configPath() string { return filepath.Join(d.Paths.ConfDir, "sing-box.json") }

func (d *SingBox) confDir() string { return filepath.Join(d.Paths.ConfDir, "sing-box") }

func (d *SingBox) instanceConfig(port int) string {
	return filepath.Join(d.confDir(), strconv.Itoa(port)+".json")
}

// EnsureInstalled implements Driver.
func (d *SingBox) EnsureInstalled(ctx context.Context, v agentproto.CoreVersion) (bool, error) {
	cur := recordedVersion(d.bin())
	if cur == "" {
		if out := installedVersion(ctx, d.bin(), "version"); out != "" {
			// "sing-box version 1.12.14\n..."
			for _, f := range strings.Fields(out) {
				if strings.Count(f, ".") >= 2 && f[0] >= '0' && f[0] <= '9' {
					cur = f
					break
				}
			}
		}
	}
	if cur == v.Version && cur != "" {
		return false, nil
	}
	if err := installBinary(ctx, d.Paths.BinDir, "sing-box", v); err != nil {
		return false, fmt.Errorf("install sing-box %s: %w", v.Version, err)
	}
	return true, nil
}

// tlsBlock builds the tls object for a node.
func (d *SingBox) tlsBlock(spec agentproto.NodeSpec, ds *agentproto.DesiredState, alpn []string) (map[string]any, error) {
	tls := map[string]any{"enabled": true}
	if len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	cert := spec.Cert
	if cert == nil {
		cert = &agentproto.CertSpec{Mode: "self_signed", Domain: ds.PublicHost}
	}
	domain := cert.Domain
	if domain == "" {
		domain = ds.PublicHost
	}
	tls["server_name"] = domain
	switch cert.Mode {
	case "acme":
		acme := map[string]any{"domain": []string{domain}, "data_directory": filepath.Join(d.Paths.DataDir, "acme"), "default_server_name": domain, "provider": "letsencrypt"}
		if email := firstNonEmpty(cert.Email, d.ACMEEmail); email != "" {
			acme["email"] = email
		}
		tls["acme"] = acme
	default:
		files, err := d.CertResolver(cert)
		if err != nil {
			return nil, fmt.Errorf("cert for %s: %w", domain, err)
		}
		tls["certificate_path"] = files.Cert
		tls["key_path"] = files.Key
	}
	return tls, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// InboundTag is the tag used for a node (parsed back by conntail).
func InboundTag(nodeID int64) string { return fmt.Sprintf("node-%d", nodeID) }

// BuildConfig renders the sing-box server configuration for nodes.
func (d *SingBox) BuildConfig(ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (map[string]any, error) {
	inbounds := []any{}
	sorted := append([]agentproto.NodeSpec(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NodeID < sorted[j].NodeID })
	logLevel := "warn"
	for _, n := range sorted {
		if n.Blocked {
			continue // no inbound = connection refused, nothing to meter
		}
		if n.ConnlogEnabled {
			logLevel = "info"
		}
		p := n.Params
		in := map[string]any{"tag": InboundTag(n.NodeID), "listen": "::", "listen_port": n.ListenPort}
		switch n.Protocol {
		case "vless":
			in["type"] = "vless"
			in["users"] = []any{map[string]any{"uuid": str(p, "uuid"), "flow": firstNonEmpty(str(p, "flow"), "xtls-rprx-vision")}}
			hsPort := 443
			if v, ok := p["handshake_port"].(float64); ok && v > 0 {
				hsPort = int(v)
			}
			in["tls"] = map[string]any{
				"enabled":     true,
				"server_name": str(p, "handshake_server"),
				"reality": map[string]any{
					"enabled":     true,
					"handshake":   map[string]any{"server": str(p, "handshake_server"), "server_port": hsPort},
					"private_key": str(p, "reality_private_key"),
					"short_id":    []string{str(p, "reality_short_id")},
				},
			}
		case "anytls":
			in["type"] = "anytls"
			in["users"] = []any{map[string]any{"password": str(p, "password")}}
			tls, err := d.tlsBlock(n, ds, nil)
			if err != nil {
				return nil, err
			}
			in["tls"] = tls
		case "hysteria2":
			in["type"] = "hysteria2"
			in["users"] = []any{map[string]any{"password": str(p, "password")}}
			if op := str(p, "obfs_password"); op != "" {
				in["obfs"] = map[string]any{"type": "salamander", "password": op}
			}
			in["masquerade"] = "https://www.bing.com"
			in["ignore_client_bandwidth"] = false
			tls, err := d.tlsBlock(n, ds, []string{"h3"})
			if err != nil {
				return nil, err
			}
			in["tls"] = tls
		case "tuic":
			in["type"] = "tuic"
			in["users"] = []any{map[string]any{"uuid": str(p, "uuid"), "password": str(p, "password")}}
			in["congestion_control"] = "bbr"
			tls, err := d.tlsBlock(n, ds, []string{"h3"})
			if err != nil {
				return nil, err
			}
			in["tls"] = tls
		case "trojan":
			in["type"] = "trojan"
			in["users"] = []any{map[string]any{"password": str(p, "password")}}
			tls, err := d.tlsBlock(n, ds, nil)
			if err != nil {
				return nil, err
			}
			in["tls"] = tls
		case "shadowsocks", "ss":
			in["type"] = "shadowsocks"
			in["method"] = firstNonEmpty(str(p, "method"), "2022-blake3-aes-128-gcm")
			in["password"] = str(p, "password")
		default:
			return nil, fmt.Errorf("sing-box driver: unsupported protocol %s", n.Protocol)
		}
		inbounds = append(inbounds, in)
	}
	strategy := "prefer_ipv4"
	if ds.IPv4Only {
		strategy = "ipv4_only"
	}
	cfg := map[string]any{
		"log": map[string]any{"level": logLevel, "timestamp": true, "output": d.Paths.LogPath()},
		"dns": map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local"}}},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route": map[string]any{
			"default_domain_resolver": map[string]any{"server": "local", "strategy": strategy},
			"rules":                   []any{map[string]any{"ip_is_private": true, "action": "reject"}},
			"final":                   "direct",
		},
	}
	return cfg, nil
}

func (d *SingBox) stopLegacy(ctx context.Context) bool {
	if !d.Systemd.IsActive(ctx, singboxUnit) {
		return false
	}
	_ = d.Systemd.StopDisable(ctx, singboxUnit)
	return true
}

func (d *SingBox) stopAllInstances(ctx context.Context) bool {
	changed := d.stopLegacy(ctx)
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service") {
		_ = d.Systemd.StopDisable(ctx, u)
		changed = true
	}
	return changed
}

// Apply implements Driver.
func (d *SingBox) Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (bool, error) {
	changed := d.stopLegacy(ctx)
	live := 0
	for _, n := range nodes {
		if !n.Blocked {
			live++
		}
	}
	if live == 0 {
		return d.stopAllInstances(ctx) || changed, nil
	}
	if err := os.MkdirAll(d.Paths.LogDir, 0o755); err != nil {
		return changed, err
	}
	env := fmt.Sprintf("Environment=GOMEMLIMIT=%dMiB", max(ds.Tuning.GoMemLimitMB, 64))
	unit := ServiceUnit("ctlvps sing-box (%i)", fmt.Sprintf("%s run -c %s/%%i.json", d.bin(), d.confDir()), ds.Tuning, env, "IPAccounting=yes", "ExecReload=/bin/kill -HUP $MAINPID")
	unitChanged, err := d.Systemd.WriteUnit(singboxTemplateUnit, unit)
	if err != nil {
		return changed, err
	}
	if unitChanged {
		changed = true
		if err := d.Systemd.DaemonReload(ctx); err != nil {
			return changed, err
		}
	}
	want := map[int]bool{}
	sorted := append([]agentproto.NodeSpec(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ListenPort < sorted[j].ListenPort })
	for _, n := range sorted {
		if n.Blocked || n.ListenPort <= 0 {
			continue
		}
		want[n.ListenPort] = true
		cfg, err := d.BuildConfig(ds, []agentproto.NodeSpec{n})
		if err != nil {
			return changed, err
		}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		c, err := WriteIfChanged(d.instanceConfig(n.ListenPort), data, 0o600)
		if err != nil {
			return changed, err
		}
		u := SingBoxUnit(n.ListenPort)
		if c || unitChanged || !d.Systemd.IsActive(ctx, u) {
			if out, err := exec.CommandContext(ctx, d.bin(), "check", "-c", d.instanceConfig(n.ListenPort)).CombinedOutput(); err != nil {
				return changed, fmt.Errorf("sing-box check :%d: %s", n.ListenPort, strings.TrimSpace(string(out)))
			}
			if err := d.Systemd.EnableRestart(ctx, u); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service") {
		portStr := strings.TrimSuffix(strings.TrimPrefix(u, "ctlvps-singbox@"), ".service")
		port, err := strconv.Atoi(portStr)
		if err != nil || want[port] {
			continue
		}
		_ = d.Systemd.StopDisable(ctx, u)
		_ = os.Remove(d.instanceConfig(port))
		changed = true
	}
	if entries, err := os.ReadDir(d.confDir()); err == nil {
		for _, e := range entries {
			p, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil && !want[p] {
				_ = os.Remove(filepath.Join(d.confDir(), e.Name()))
			}
		}
	}
	_ = os.Remove(d.configPath())
	return changed, nil
}

// Status implements Driver (aggregated over instances).
func (d *SingBox) Status(ctx context.Context) agentproto.CoreStatus {
	st := agentproto.CoreStatus{Name: "sing-box", Version: recordedVersion(d.bin())}
	_, err := os.Stat(d.bin())
	st.Installed = err == nil
	if st.Installed && st.Version == "" {
		st.Version = strings.TrimSpace(strings.TrimPrefix(installedVersion(ctx, d.bin(), "version"), "sing-box version "))
		if i := strings.IndexByte(st.Version, '\n'); i > 0 {
			st.Version = st.Version[:i]
		}
	}
	units := d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service")
	if len(units) == 0 && d.Systemd.IsActive(ctx, singboxUnit) {
		one := d.Systemd.Show(ctx, singboxUnit)
		one.Name, one.Version, one.Installed = st.Name, st.Version, st.Installed
		return one
	}
	st.Instances = len(units)
	active := 0
	for _, u := range units {
		s := d.Systemd.Show(ctx, u)
		if s.Active {
			active++
		}
		st.RSSBytes += s.RSSBytes
		st.NRestarts += s.NRestarts
		if s.LastError != "" && st.LastError == "" {
			st.LastError = u + ": " + s.LastError
		}
		if st.Since.IsZero() || (!s.Since.IsZero() && s.Since.Before(st.Since)) {
			st.Since = s.Since
		}
	}
	st.Active = len(units) > 0 && active == len(units)
	if len(units) > 0 && active < len(units) && st.LastError == "" {
		st.LastError = fmt.Sprintf("%d/%d instances active", active, len(units))
	}
	if !st.Active && st.Installed && st.LastError == "" {
		if lines := d.Systemd.JournalTail(ctx, singboxTemplateUnit, 3); len(lines) > 0 {
			st.LastError = lines[len(lines)-1]
		}
	}
	return st
}

// Stop implements Driver.
func (d *SingBox) Stop(ctx context.Context) error {
	d.stopAllInstances(ctx)
	return nil
}

// Certs reports certificate expiry for self-signed/external nodes.
func (d *SingBox) Certs(nodes []agentproto.NodeSpec) []agentproto.CertStatus {
	var out []agentproto.CertStatus
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Cert == nil || n.Cert.Mode == "acme" || seen[n.Cert.Domain] {
			continue
		}
		seen[n.Cert.Domain] = true
		files, err := d.CertResolver(n.Cert)
		if err != nil {
			continue
		}
		if info, ok := CertInfo(files.Cert, n.Cert.Domain, n.Cert.Mode); ok {
			out = append(out, info)
		}
	}
	return out
}

// RecentErrors returns the newest warning+ lines of the sing-box log.
func (d *SingBox) RecentErrors(n int) []string {
	data, err := os.ReadFile(d.Paths.LogPath())
	if err != nil {
		return nil
	}
	if len(data) > 256<<10 {
		data = data[len(data)-256<<10:]
	}
	var out []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		l := string(line)
		if strings.Contains(l, " ERROR ") || strings.Contains(l, " FATAL ") || strings.Contains(l, " WARN ") {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
