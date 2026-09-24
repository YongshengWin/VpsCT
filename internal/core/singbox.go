package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/nft"
	"ctlvps/internal/wgconfig"
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
	private       bool
	runtimeACME   string
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
		acme := map[string]any{"domain": []string{domain}, "data_directory": firstNonEmpty(d.runtimeACME, filepath.Join(d.Paths.DataDir, "acme")), "default_server_name": domain, "provider": "letsencrypt"}
		if email := firstNonEmpty(cert.Email, d.ACMEEmail); email != "" {
			acme["email"] = email
		}
		if corecompat.ModernConfig(ds.Versions["sing-box"].Version) {
			acme["type"] = "acme"
			tls["certificate_provider"] = acme
		} else {
			tls["acme"] = acme
		}
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
	endpoints := []any{}
	localDNS := map[string]any{"type": "local", "tag": "local"}
	// Since 1.13, local DNS may select D-Bus even on hosts without
	// systemd-resolved. Use the system nameservers directly on those versions.
	if major, minor, _, ok := corecompat.StableVersion(ds.Versions["sing-box"].Version); ok && major == 1 && minor >= 13 {
		localDNS["prefer_go"] = true
	}
	dnsServers := []any{localDNS}
	rules := []any{}
	denied := []string{}
	for _, n := range sorted {
		if !n.AllowPrivate {
			denied = append(denied, InboundTag(n.NodeID))
		}
	}
	if len(denied) > 0 {
		rules = append(rules, map[string]any{"inbound": denied, "ip_is_private": true, "action": "reject"})
	}
	for _, n := range sorted {
		// Access is enforced by nftables, independently of process lifetime.
		if err := validateNodeNetwork(n, ds); err != nil {
			return nil, fmt.Errorf("node %d: %w", n.NodeID, err)
		}
		mark, err := nft.NodeMark(n.NodeID)
		if err != nil {
			return nil, err
		}
		outTag := fmt.Sprintf("node-%d-direct", n.NodeID)
		outbound := map[string]any{"type": "direct", "tag": outTag, "routing_mark": mark}
		dial := map[string]any{"routing_mark": mark}
		listen := "::"
		if n.RuntimeNetwork != nil {
			listen = n.RuntimeNetwork.ListenAddress
			if n.Network.HasTransport() {
				transport, _ := n.Network.TransportConfig()
				cfg, err := networkconfig.EffectiveSOCKS5(transport, ds.IPv4Only)
				if err != nil {
					return nil, err
				}
				// Use exactly the endpoint checked by the local guard. The
				// bootstrap resolver must never choose a different address.
				cfg.Server = n.RuntimeNetwork.SOCKS5.Address
				var compiled SOCKS5Compilation
				if n.Network.WireGuard != nil {
					compiled, err = CompileWireGuard(n.NodeID, n.Network.WireGuard.Config, n.Network.WireGuard.Credentials, cfg, *n.RuntimeNetwork.Direct)
				} else if n.Network.SSH != nil {
					compiled, err = CompileSSH(n.NodeID, n.Network.SSH.Config, n.Network.SSH.Credentials, cfg, *n.RuntimeNetwork.Direct)
				} else if n.Network.SS2022 != nil {
					if !corecompat.SS2022Outbound(ds.Versions["sing-box"].Version) {
						return nil, errors.New(corecompat.SS2022Requirement)
					}
					compiled, err = CompileResourceSS2022(agentproto.ResourceIdentity{Kind: "node", ID: n.NodeID}, n.Network.SS2022.Config, n.Network.SS2022.Credentials, cfg, *n.RuntimeNetwork.Direct)
				} else {
					compiled, err = CompileSOCKS5(n.NodeID, cfg, n.Network.SOCKS5.Credentials, *n.RuntimeNetwork.Direct)
				}
				if err != nil {
					return nil, err
				}
				outbound = compiled.Outbound
				outTag = compiled.Outbound["tag"].(string)
				dial = map[string]any{"detour": outTag}
				dnsServers = append(dnsServers, compiled.BootstrapDNS, compiled.DNS)
				if compiled.TransportOutbound != nil {
					outbounds = append(outbounds, compiled.TransportOutbound)
				}
				if compiled.UDPReject != nil {
					rules = append(rules, compiled.UDPReject)
				}
				if compiled.FamilyReject != nil {
					rules = append(rules, compiled.FamilyReject)
				}
				rules = append(rules, compiled.ResolveRule)
				if !n.AllowPrivate {
					rules = append(rules, map[string]any{"inbound": []string{InboundTag(n.NodeID)}, "ip_is_private": true, "action": "reject"})
				}
			} else if n.RuntimeNetwork.Direct != nil {
				compiled, err := CompileDirect(n.NodeID, *n.RuntimeNetwork.Direct)
				if err != nil {
					return nil, err
				}
				outbound, dial = compiled.Outbound, compiled.Dial
				dnsServers = append(dnsServers, compiled.DNS)
				rules = append(rules, compiled.ResolveRule)
				if compiled.FamilyReject != nil {
					rules = append(rules, compiled.FamilyReject)
				}
				if !n.AllowPrivate {
					// Recheck after explicit resolution, including DNS answers
					// which point to private networks.
					rules = append(rules, map[string]any{"inbound": []string{InboundTag(n.NodeID)}, "ip_is_private": true, "action": "reject"})
				}
			}
		}
		if n.Network != nil && n.Network.WireGuard != nil {
			endpoints = append(endpoints, outbound)
			rules = append(rules, map[string]any{"inbound": []string{outTag}, "action": "reject"})
		} else {
			outbounds = append(outbounds, outbound)
		}
		rules = append(rules, map[string]any{"inbound": []string{InboundTag(n.NodeID)}, "action": "route", "outbound": outTag})
		if n.ConnlogEnabled {
			logLevel = "info"
		}
		p := n.Params
		in := map[string]any{"tag": InboundTag(n.NodeID), "listen": listen, "listen_port": n.ListenPort}
		switch n.Protocol {
		case "wireguard":
			if n.Network != nil {
				return nil, errors.New("WireGuard 接入暂不支持再次串接出口")
			}
			if ds.NetworkWireGuardVersion != agentproto.NetworkWireGuardVersion || !agentproto.NetworkBindingSupported("singbox", ds.Versions["sing-box"].Version) {
				return nil, errors.New("WireGuard 接入需要兼容的 agent 与锁定内核")
			}
			server, err := wgconfig.DecodeServer(p)
			if err != nil {
				return nil, err
			}
			endpoints = append(endpoints, server.Endpoint(InboundTag(n.NodeID), n.ListenPort))
			continue
		case "vless":
			in["type"] = "vless"
			in["users"] = []any{map[string]any{"uuid": str(p, "uuid"), "flow": firstNonEmpty(str(p, "flow"), "xtls-rprx-vision")}}
			hsPort := 443
			if v, ok := p["handshake_port"].(float64); ok && v > 0 {
				hsPort = int(v)
			}
			handshake := copyFields(dial)
			handshake["server"], handshake["server_port"] = str(p, "handshake_server"), hsPort
			in["tls"] = map[string]any{
				"enabled":     true,
				"server_name": str(p, "handshake_server"),
				"reality": map[string]any{
					"enabled":     true,
					"handshake":   handshake,
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
		"log":       map[string]any{"level": logLevel, "timestamp": true, "output": d.logPath()},
		"dns":       map[string]any{"servers": dnsServers},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"default_domain_resolver": map[string]any{"server": "local", "strategy": strategy},
			"rules":                   rules,
		},
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}
	if len(dnsServers) > 1 && !corecompat.ModernConfig(ds.Versions["sing-box"].Version) {
		cfg["dns"].(map[string]any)["independent_cache"] = true
	}
	return cfg, nil
}

func (d *SingBox) stopAllInstances(ctx context.Context) (bool, error) {
	units := d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service")
	if _, err := os.Stat(filepath.Join(d.Systemd.UnitDir, singboxUnit)); err == nil || d.Systemd.IsActive(ctx, singboxUnit) {
		units = append(units, singboxUnit)
	}
	if _, e := os.Stat(filepath.Join(d.Systemd.UnitDir, "ctlvps-singbox-private.service")); e == nil || d.Systemd.IsActive(ctx, "ctlvps-singbox-private.service") {
		units = append(units, "ctlvps-singbox-private.service")
	}
	var errs []error
	for _, u := range units {
		if err := d.Systemd.StopDisable(ctx, u); err != nil {
			errs = append(errs, err)
		}
	}
	return len(units) > 0, errors.Join(errs...)
}

// Apply preflights all permission groups before the first running service stops.
func (d *SingBox) Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (changed bool, applyErr error) {
	return d.ApplyResources(ctx, ds, nodes, nil)
}

// ApplyResources receives only locally prepared forwards. They join the public
// shared service and cannot inherit a node's private-business permission.
func (d *SingBox) ApplyResources(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) (changed bool, applyErr error) {
	groups := map[string][]agentproto.NodeSpec{"public": {}, "private": {}}
	acme := false
	for _, n := range nodes {
		if n.Retired {
			continue
		}
		profile := "public"
		if n.AllowPrivate {
			profile = "private"
		}
		groups[profile] = append(groups[profile], n)
		if n.Cert != nil && n.Cert.Mode == "acme" {
			acme = true
		}
	}
	hasLive := func(ns []agentproto.NodeSpec) bool {
		for _, n := range ns {
			if !n.Blocked {
				return true
			}
		}
		return false
	}
	liveForwards := false
	for _, f := range forwards {
		liveForwards = liveForwards || (!f.Blocked && !f.Retired)
	}
	twoGroups := (hasLive(groups["public"]) || liveForwards) && hasLive(groups["private"])
	// Splitting native ACME storage/challenge listeners is not safe without a
	// separate compatibility test. Refuse before changing a running service.
	if acme && twoGroups {
		return false, fmt.Errorf("mixed private/public ACME groups need explicit compatibility validation; existing services retained")
	}
	if twoGroups {
		budget := ds.Tuning.MemoryMaxMB
		if budget <= 0 {
			budget = 256
		}
		minimum := 2 * (max(ds.Tuning.GoMemLimitMB, 64) + 32)
		if budget < minimum {
			return false, fmt.Errorf("proxy memory budget insufficient for two permission groups; existing services retained")
		}
	}
	launcher, err := proxyLauncher()
	if err != nil {
		return false, err
	}
	if err = d.Systemd.EnsureProxyGuard(ctx, launcher); err != nil {
		return false, err
	}
	type candidate struct {
		profile, unit, config, body string
		data                        []byte
		gid                         int
		active                      bool
	}
	candidates := []candidate{}
	for _, profile := range []string{"public", "private"} {
		live := profile == "public" && liveForwards
		for _, n := range groups[profile] {
			if !n.Blocked {
				live = true
			}
		}
		if !live {
			continue
		}
		user := "ctlvps-sb"
		if profile == "private" {
			user = "ctlvps-sp"
		}
		uid, gid, e := proxyIdentity(user)
		if e != nil {
			return false, e
		}
		dir := filepath.Join(proxyConfigRoot, profile)
		if e = secureDir(dir, 0, int(gid), 0750); e != nil {
			return false, e
		}
		clone := *d
		clone.private = profile == "private"
		origResolver := d.CertResolver
		clone.CertResolver = func(spec *agentproto.CertSpec) (CertFiles, error) {
			f, e := origResolver(spec)
			if e != nil {
				return f, e
			}
			return snapshotCert(f, dir, int(gid))
		}
		if e = secureDir(d.Paths.LogDir, 0, 0, 0755); e != nil {
			return false, e
		}
		log := clone.logPath()
		if st, e := os.Lstat(log); e == nil && !st.Mode().IsRegular() {
			return false, fmt.Errorf("unsafe proxy log")
		}
		f, e := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return false, e
		}
		e = f.Chown(int(uid), int(gid))
		f.Close()
		if e != nil {
			return false, e
		}
		source, target := "", ""
		for _, n := range groups[profile] {
			if n.Cert != nil && n.Cert.Mode == "acme" {
				source = filepath.Join(d.Paths.DataDir, "acme")
				target = filepath.Join(proxyStateRoot, profile, "acme")
				break
			}
		}
		if source != "" {
			marker := filepath.Join(dir, "acme-ready")
			if _, e = os.Stat(marker); os.IsNotExist(e) {
				if e = prepareACME(source, int(uid), int(gid)); e != nil {
					return false, e
				}
				if _, e = proxyFile(marker, []byte(source), int(gid)); e != nil {
					return false, e
				}
			}
			if e = secureDir(target, 0, 0, 0755); e != nil {
				return false, e
			}
			clone.runtimeACME = target
		}
		var managedForwards []agentproto.ForwardSpec
		if profile == "public" {
			managedForwards = forwards
		}
		cfg, e := clone.BuildResourceConfig(ds, groups[profile], managedForwards)
		if e != nil {
			return false, e
		}
		data, e := json.MarshalIndent(cfg, "", "  ")
		if e != nil {
			return false, e
		}
		if _, e = proxyFile(filepath.Join(dir, "candidate.json"), data, int(gid)); e != nil {
			return false, e
		}
		props := proxyProperties(user, dir, log, SingBoxSlice(clone.private), source, target, true)
		if _, e = d.Systemd.EnsureSingBoxSlice(ctx, clone.private); e != nil {
			return false, e
		}
		if e = d.Systemd.CheckProxy(ctx, launcher, profile, props); e != nil {
			return false, e
		}
		props = append(props, fmt.Sprintf("Environment=GOMEMLIMIT=%dMiB", max(ds.Tuning.GoMemLimitMB, 64)), "IPAccounting=yes")
		unit := singboxUnit
		if clone.private {
			unit = "ctlvps-singbox-private.service"
		}
		body := ServiceUnit("ctlvps sing-box", launcher+" proxy-exec singbox run "+profile, ds.Tuning, props...)
		config := filepath.Join(dir, "config.json")
		old, _ := os.ReadFile(config)
		oldUnit, _ := os.ReadFile(filepath.Join(d.Systemd.UnitDir, unit))
		active := d.Systemd.IsActive(ctx, unit)
		if !bytes.Equal(old, data) || string(oldUnit) != body || !active {
			changed = true
		}
		candidates = append(candidates, candidate{profile, unit, config, body, data, int(gid), active})
	}
	units := []string{singboxUnit, "ctlvps-singbox-private.service"}
	units = append(units, d.Systemd.ListUnits(ctx, "ctlvps-singbox@*.service")...)
	want := map[string]bool{}
	for _, c := range candidates {
		want[c.unit] = true
	}
	for _, u := range units {
		if !want[u] && d.Systemd.IsActive(ctx, u) {
			changed = true
		}
	}
	changed = changed || d.binaryChanged || activationPending(d.bin())
	if !changed {
		for _, c := range candidates {
			account := "ctlvps-sb"
			if c.profile == "private" {
				account = "ctlvps-sp"
			}
			if e := d.Systemd.CheckRunningProxy(ctx, c.unit, account, SingBoxSlice(c.profile == "private"), launcher+" proxy-exec singbox run "+c.profile, true); e != nil {
				return false, e
			}
		}
		return false, nil
	}
	type saved struct {
		path   string
		data   []byte
		exists bool
		mode   os.FileMode
		gid    int
	}
	backups := []saved{}
	active := []string{}
	enabledBefore := map[string]bool{}
	for _, u := range units {
		en, _ := d.Systemd.ctl(ctx, "is-enabled", u)
		enabledBefore[u] = strings.TrimSpace(en) == "enabled"
		p := filepath.Join(d.Systemd.UnitDir, u)
		b, e := os.ReadFile(p)
		backups = append(backups, saved{p, b, e == nil, 0644, -1})
		if d.Systemd.IsActive(ctx, u) {
			active = append(active, u)
		}
	}
	for _, c := range candidates {
		b, e := os.ReadFile(c.config)
		backups = append(backups, saved{c.config, b, e == nil, 0640, c.gid})
	}
	defer func() {
		if applyErr == nil {
			return
		}
		r, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		stop := []string{}
		for _, u := range units {
			if _, e := os.Stat(filepath.Join(d.Systemd.UnitDir, u)); e == nil || d.Systemd.IsActive(r, u) {
				stop = append(stop, u)
			}
		}
		errs := []error{applyErr, d.Systemd.StopUnits(r, stop)}
		for _, u := range stop {
			if !enabledBefore[u] {
				_, e := d.Systemd.ctl(r, "disable", u)
				errs = append(errs, e)
			}
		}
		for _, b := range backups {
			if b.exists {
				_, e := WriteIfChanged(b.path, b.data, b.mode)
				errs = append(errs, e)
				if b.gid >= 0 {
					errs = append(errs, os.Chown(b.path, 0, b.gid))
				}
			} else {
				if e := os.Remove(b.path); e != nil && !os.IsNotExist(e) {
					errs = append(errs, e)
				}
			}
		}
		errs = append(errs, d.Systemd.DaemonReload(r))
		for _, u := range active {
			errs = append(errs, d.Systemd.StartUnits(r, []string{u}))
		}
		applyErr = errors.Join(errs...)
	}()
	for _, u := range active {
		if err = d.Systemd.StopUnits(ctx, []string{u}); err != nil {
			return false, err
		}
	}
	for _, c := range candidates {
		if _, err = proxyFile(c.config, c.data, c.gid); err != nil {
			return false, err
		}
		if _, err = d.Systemd.WriteUnit(c.unit, c.body); err != nil {
			return false, err
		}
	}
	if err = d.Systemd.DaemonReload(ctx); err != nil {
		return false, err
	}
	for _, c := range candidates {
		if err = d.Systemd.EnableRestart(ctx, c.unit); err != nil {
			return false, err
		}
	}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
	}
	for _, c := range candidates {
		if !d.Systemd.IsActive(ctx, c.unit) {
			return false, fmt.Errorf("isolated sing-box activation failed")
		}
		user := "ctlvps-sb"
		if c.profile == "private" {
			user = "ctlvps-sp"
		}
		if err = d.Systemd.CheckRunningProxy(ctx, c.unit, user, SingBoxSlice(c.profile == "private"), launcher+" proxy-exec singbox run "+c.profile, true); err != nil {
			return false, err
		}
	}
	for _, u := range units {
		if !want[u] {
			if _, e := os.Stat(filepath.Join(d.Systemd.UnitDir, u)); os.IsNotExist(e) && !d.Systemd.IsActive(ctx, u) {
				continue
			}
			if err = d.Systemd.StopDisable(ctx, u); err != nil {
				return false, err
			}
		}
	}
	if err := completeActivation(d.bin()); err != nil {
		return true, err
	}
	d.binaryChanged = false
	return true, nil
}

func (d *SingBox) logPath() string {
	if d.private {
		return filepath.Join(d.Paths.LogDir, "sing-box-private.log")
	}
	return d.Paths.LogPath()
}

func (d *SingBox) Status(ctx context.Context) agentproto.CoreStatus {
	st := agentproto.CoreStatus{Name: "sing-box", Version: recordedVersion(d.bin())}
	_, e := os.Stat(d.bin())
	st.Installed = e == nil
	active := 0
	for _, unit := range []string{singboxUnit, "ctlvps-singbox-private.service"} {
		current := d.Systemd.Show(ctx, unit)
		enabled, _ := d.Systemd.ctl(ctx, "is-enabled", unit)
		if strings.TrimSpace(enabled) != "enabled" && !current.Active {
			continue
		}
		st.Instances++
		if current.Active {
			active++
		}
		st.RSSBytes += current.RSSBytes
		st.NRestarts += current.NRestarts
		if current.LastError != "" {
			st.LastError = current.LastError
		}
		if st.Since.IsZero() || (!current.Since.IsZero() && current.Since.Before(st.Since)) {
			st.Since = current.Since
		}
	}
	st.Active = st.Instances > 0 && active == st.Instances
	if st.Instances > 0 && !st.Active && st.LastError == "" {
		st.LastError = fmt.Sprintf("%d/%d instances active", active, st.Instances)
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
	out := recentLogErrors(d.Paths.LogPath(), n)
	out = append(out, recentLogErrors(filepath.Join(d.Paths.LogDir, "sing-box-private.log"), n)...)
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func recentLogErrors(path string, n int) []string {
	f, err := os.Open(path)
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
