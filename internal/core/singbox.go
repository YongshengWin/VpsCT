package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/nft"
)

// SingBox drives a single sing-box service hosting all sing-box nodes.
type SingBox struct {
	Paths   Paths
	Systemd *Systemd
	// CertResolver returns cert/key paths for a node (self-signed/external).
	// It is injected so config generation is testable without disk access.
	CertResolver func(spec *agentproto.CertSpec) (CertFiles, error)
	// Email used for ACME registrations (optional).
	ACMEEmail     string
	binaryChanged bool
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
	singboxUnit         = "ctlvps-singbox.service" // shared process
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
	d.binaryChanged = true
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
	outbounds := []any{}
	rules := []any{map[string]any{"ip_is_private": true, "action": "reject"}}
	for _, n := range sorted {
		// Access is enforced by nftables, independently of process lifetime.
		mark, err := nft.NodeMark(n.NodeID)
		if err != nil {
			return nil, err
		}
		outTag := fmt.Sprintf("node-%d-direct", n.NodeID)
		outbounds = append(outbounds, map[string]any{"type": "direct", "tag": outTag, "routing_mark": mark})
		rules = append(rules, map[string]any{"inbound": []string{InboundTag(n.NodeID)}, "action": "route", "outbound": outTag})
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
					"handshake":   map[string]any{"server": str(p, "handshake_server"), "server_port": hsPort, "routing_mark": mark},
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
			// A local decoy avoids unassigned outbound traffic on failed auth.
			in["masquerade"] = map[string]any{"type": "string", "status_code": 404, "content": "Not Found"}
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
		"log":       map[string]any{"level": logLevel, "timestamp": true, "output": d.Paths.LogPath()},
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local"}}},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"default_domain_resolver": map[string]any{"server": "local", "strategy": strategy},
			"rules":                   rules,
		},
	}
	return cfg, nil
}

func (d *SingBox) stopAllInstances(ctx context.Context) (bool, error) {
	units := d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service")
	if _, err := os.Stat(filepath.Join(d.Systemd.UnitDir, singboxUnit)); err == nil || d.Systemd.IsActive(ctx, singboxUnit) {
		units = append(units, singboxUnit)
	}
	var errs []error
	for _, u := range units {
		if err := d.Systemd.StopDisable(ctx, u); err != nil {
			errs = append(errs, err)
		}
	}
	return len(units) > 0, errors.Join(errs...)
}

// Apply validates a complete candidate before touching a running service.
// The agent installs node accounting/firewall rules before invoking this method.
func (d *SingBox) Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (applied bool, applyErr error) {
	live := 0
	for _, n := range nodes {
		if !n.Blocked {
			live++
		}
	}
	if live == 0 {
		return d.stopAllInstances(ctx)
	}
	if err := os.MkdirAll(d.Paths.LogDir, 0755); err != nil {
		return false, err
	}
	cfg, err := d.BuildConfig(ds, nodes)
	if err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	candidate := d.configPath() + ".candidate"
	if _, err = WriteIfChanged(candidate, data, 0600); err != nil {
		return false, err
	}
	defer os.Remove(candidate)
	if out, e := exec.CommandContext(ctx, d.bin(), "check", "-c", candidate).CombinedOutput(); e != nil {
		return false, fmt.Errorf("sing-box candidate rejected: %v: %s", e, strings.TrimSpace(string(out)))
	}
	old, readErr := os.ReadFile(d.configPath())
	unitPath := filepath.Join(d.Systemd.UnitDir, singboxUnit)
	oldUnit, _ := os.ReadFile(unitPath)
	env := fmt.Sprintf("Environment=GOMEMLIMIT=%dMiB", max(ds.Tuning.GoMemLimitMB, 64))
	unit := ServiceUnit("ctlvps sing-box", fmt.Sprintf("%s run -c %s", d.bin(), d.configPath()), ds.Tuning, env, "IPAccounting=yes")
	changed := d.binaryChanged || !bytes.Equal(old, data) || string(oldUnit) != unit || !d.Systemd.IsActive(ctx, singboxUnit)
	if !changed {
		return false, nil
	}

	// Capture running services before any destructive step, and restore on
	// every error path, including filesystem/daemon-reload failures.
	legacy := d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service")
	activeLegacy := []string{}
	for _, u := range legacy {
		if d.Systemd.IsActive(ctx, u) {
			activeLegacy = append(activeLegacy, u)
		}
	}
	wasActive := d.Systemd.IsActive(ctx, singboxUnit)
	defer func() {
		if applyErr == nil {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		failures := []error{applyErr}
		if e := d.Systemd.StopUnits(c, []string{singboxUnit}); e != nil {
			failures = append(failures, e)
		}
		if readErr == nil {
			_, e := WriteIfChanged(d.configPath(), old, 0600)
			failures = append(failures, e)
		} else {
			_ = os.Remove(d.configPath())
		}
		if len(oldUnit) > 0 {
			_, e := WriteIfChanged(unitPath, oldUnit, 0644)
			failures = append(failures, e)
		} else {
			_ = os.Remove(unitPath)
		}
		failures = append(failures, d.Systemd.DaemonReload(c))
		if wasActive {
			failures = append(failures, d.Systemd.EnableRestart(c, singboxUnit))
		}
		for _, u := range activeLegacy {
			failures = append(failures, d.Systemd.EnableRestart(c, u))
		}
		applyErr = errors.Join(failures...)
	}()

	for _, u := range legacy {
		if err = d.Systemd.StopDisable(ctx, u); err != nil {
			return false, err
		}
	}
	if _, err = WriteIfChanged(d.configPath(), data, 0600); err != nil {
		return false, err
	}
	if _, err = d.Systemd.WriteUnit(singboxUnit, unit); err != nil {
		return false, err
	}
	if err = d.Systemd.DaemonReload(ctx); err == nil {
		err = d.Systemd.EnableRestart(ctx, singboxUnit)
	}
	if err == nil {
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-timer.C:
		}
		timer.Stop()
	}
	if err == nil && !d.Systemd.IsActive(ctx, singboxUnit) {
		err = fmt.Errorf("shared sing-box did not become active")
	}
	if err != nil {
		return false, fmt.Errorf("shared sing-box activation failed: %w", err)
	}
	d.binaryChanged = false

	return true, nil
}

func (d *SingBox) Status(ctx context.Context) agentproto.CoreStatus {
	st := d.Systemd.Show(ctx, singboxUnit)
	st.Name, st.Version = "sing-box", recordedVersion(d.bin())
	_, err := os.Stat(d.bin())
	st.Installed = err == nil
	if st.Active {
		st.Instances = 1
	}
	return st
}

func (d *SingBox) Stop(ctx context.Context) error { _, err := d.stopAllInstances(ctx); return err }

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
	f, err := os.Open(d.Paths.LogPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, e := f.Stat(); e == nil && st.Size() > 256<<10 {
		_, _ = f.Seek(-(256 << 10), 2)
	}
	data, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return nil
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
