// Package provision creates deployable nodes: it generates credentials and
// splits them into client-facing params (Clash vocabulary) and server-only
// params consumed by the agent's core drivers.
package provision

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/netip"
	"strings"

	"golang.org/x/crypto/curve25519"

	"ctlvps/internal/domain"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/nodecombo"
	"ctlvps/internal/wgconfig"
)

// Options tune node creation.
type Options struct {
	VLESS          *nodecombo.VLESS
	Trojan         *nodecombo.Trojan
	Network        *networkconfig.Node
	AdvertiseHost  string
	Name           string
	Protocol       string
	Port           int    // 0 = allocate
	SNI            string // reality handshake target / TLS server name
	Domain         string // TLS domain when cert mode is acme/external; empty = public host
	Obfs           bool   // hysteria2 salamander
	SnellVersion   int
	SSMethod       string
	SSNetwork      string // empty/both = TCP+UDP; tcp = TCP only
	MieruTransport string
	CertID         string
	CertMode       string // inherit server when empty
}

// Ports range used for automatic allocation.
const (
	PortMin = 20000
	PortMax = 50000
)

// AllocatePort picks a free random port.
func AllocatePort(used map[int]bool) (int, error) {
	for i := 0; i < 500; i++ {
		p := PortMin + mrand.IntN(PortMax-PortMin)
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("no free port")
}

// AllocateNodePort keeps legacy allocation on ordinary hosts, but a NAT host
// uses only its confirmed provider pool with every required transport present.
func AllocateNodePort(used map[int]bool, config *natconfig.Config, protocol string, params map[string]any) (int, error) {
	if config == nil {
		return AllocatePort(used)
	}
	if protocol == "mieru" && (params == nil || params["transport"] == nil || params["transport"] == "") {
		params = map[string]any{"transport": "TCP"}
	}
	networks, err := natconfig.ProtocolNetworks(protocol, params)
	if err != nil {
		return 0, err
	}
	pools, err := config.Pools()
	if err != nil {
		return 0, err
	}
	key := networks[0]
	if len(networks) > 1 {
		key = "both"
	}
	pairs, err := config.Expand()
	if err != nil {
		return 0, err
	}
	blocked := map[int]bool{}
	for _, p := range pairs {
		if p.PublicPort != p.LocalPort {
			blocked[p.LocalPort] = true
		}
	}
	available := []int{}
	for _, port := range pools[key] {
		if !used[port] && !blocked[port] && (protocol != "mieru" || port >= 1025) {
			available = append(available, port)
		}
	}
	if len(available) == 0 {
		return 0, errors.New("商家 NAT 映射中没有支持该协议的可用公网端口")
	}
	return available[mrand.IntN(len(available))], nil
}

// UUID returns a random v4 UUID string.
func UUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Password returns a URL-safe random secret.
func Password(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SS2022Key returns a base64 key of n bytes for Shadowsocks 2022 ciphers.
func SS2022Key(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// RealityKeyPair returns (private, public) in sing-box/xray base64url form.
func RealityKeyPair() (string, string, error) {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv), base64.RawURLEncoding.EncodeToString(pub), nil
}

// ShortID returns an 8-hex-char reality short id.
func ShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DefaultSNI is the reality handshake target when none is given.
const DefaultSNI = "www.sony.com"

// NewNode builds a deployed node for server. The caller assigns ownership,
// share id and persists it.
func NewNode(server domain.Server, host string, opts Options) (domain.Node, error) {
	var network *networkconfig.Node
	if opts.Network != nil {
		copy := *opts.Network
		if err := copy.Validate(); err != nil {
			return domain.Node{}, err
		}
		network = &copy
		if copy.AdvertiseMode == "override" {
			if err := networkconfig.ValidateAdvertiseHost(opts.AdvertiseHost); err != nil {
				return domain.Node{}, err
			}
			host = opts.AdvertiseHost
		} else if opts.AdvertiseHost != "" {
			return domain.Node{}, errors.New("独立访问地址需要选择 override 模式")
		} else if copy.AdvertiseMode == "inherit_ipv6" {
			host = server.PublicHostIPv6
		}
		if copy.IngressFamily == "ipv6" {
			var err error
			host, err = networkconfig.NormalizeIPv6Host(host)
			if err != nil {
				return domain.Node{}, err
			}
		}
	} else if opts.AdvertiseHost != "" {
		return domain.Node{}, errors.New("独立访问地址需要显式网络策略")
	}
	if opts.Port <= 0 || opts.Port > 65535 {
		return domain.Node{}, errors.New("port required")
	}
	if !domain.ProtocolAllowed(opts.Protocol, server.CoreMode) {
		if opts.Protocol == domain.ProtocolSnell {
			return domain.Node{}, fmt.Errorf("服务器 %s 为精简模式（仅 sing-box），不支持 Snell", server.Name)
		}
		return domain.Node{}, fmt.Errorf("协议 %s 不支持自动部署", opts.Protocol)
	}
	if host == "" {
		host = server.PublicHost
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = DefaultNodeName(server, opts.Protocol)
	}
	certMode := opts.CertMode
	if certMode == "" {
		certMode = server.CertMode
	}
	if certMode == "" {
		certMode = "self_signed"
	}
	tlsDomain := opts.Domain
	if tlsDomain == "" {
		tlsDomain = host
	}
	core := domain.CoreFor(opts.Protocol, server.CoreMode)
	if opts.VLESS != nil && opts.Protocol != domain.ProtocolVLESS {
		return domain.Node{}, errors.New("VLESS 选项不能用于其他协议")
	}
	if opts.Trojan != nil && opts.Protocol != domain.ProtocolTrojan {
		return domain.Node{}, errors.New("Trojan 选项不能用于其他协议")
	}
	managedTLS := opts.VLESS != nil && opts.VLESS.Security == "tls" || opts.Trojan != nil
	if managedTLS {
		if network != nil && (opts.VLESS != nil || opts.Trojan.Transport == "ws" || opts.Trojan.Transport == "grpc") {
			return domain.Node{}, errors.New("普通 TLS 新组合暂不支持自定义网络策略")
		}
		if opts.SNI != "" {
			if opts.Domain != "" && opts.Domain != opts.SNI {
				return domain.Node{}, errors.New("普通 TLS 的 SNI 必须与证书名称一致")
			}
			tlsDomain = opts.SNI
		}
		if err := nodecombo.ValidateManagedCert(certMode, tlsDomain, opts.CertID); err != nil {
			return domain.Node{}, err
		}
	}
	client := map[string]any{}
	srv := map[string]any{"cert_mode": certMode, "tls_domain": tlsDomain}
	switch opts.Protocol {
	case domain.ProtocolAnyTLS, domain.ProtocolHysteria2, domain.ProtocolTUIC, domain.ProtocolTrojan:
		if network != nil && network.IngressFamily == "ipv6" && certMode == "acme" {
			_, ipErr := netip.ParseAddr(tlsDomain)
			if ipErr == nil || !strings.Contains(tlsDomain, ".") || networkconfig.ValidateAdvertiseHost(tlsDomain) != nil {
				return domain.Node{}, errors.New("仅 IPv6 节点使用 ACME 时需要有效的 TLS 域名")
			}
		}
	}
	if certMode == "external" && (managedTLS || opts.Protocol == "anytls" || opts.Protocol == "hysteria2" || opts.Protocol == "tuic" || opts.Protocol == "trojan") {
		if !nodecombo.ValidCertID(opts.CertID) {
			return domain.Node{}, errors.New("外部证书需要本机策略中登记的证书 ID")
		}
		srv["cert_id"] = opts.CertID
	}
	insecure := certMode == "self_signed"
	switch opts.Protocol {
	case domain.ProtocolVLESS:
		var combo nodecombo.VLESS
		if opts.VLESS != nil {
			combo = *opts.VLESS
		}
		combo, err := combo.Normalized()
		if err != nil {
			return domain.Node{}, err
		}
		if combo.Security == "tls" {
			uuid := UUID()
			client["uuid"], srv["uuid"] = uuid, uuid
			client["tls"], client["servername"], client["udp"] = true, tlsDomain, true
			if insecure {
				client["skip-cert-verify"] = true
			}
			setTLSTransport(client, srv, combo.Transport, combo.WS, combo.GRPC)
			break
		}
		if combo.Transport == "xhttp" {
			if server.CoreMode != domain.CoreModeStable || server.IsNAT || opts.Network != nil {
				return domain.Node{}, errors.New("XHTTP 仅支持标准模式、独立公网和普通接入，不能指定网络策略")
			}
			if opts.SNI != "" && !nodecombo.ValidDNSName(opts.SNI) {
				return domain.Node{}, errors.New("REALITY SNI 必须是域名")
			}
			core = domain.CoreXray
			x := combo.XHTTP
			srv["network"] = "xhttp"
			srv["xhttp_path"] = x.Path
			srv["xhttp_mode"] = x.Mode
			xc := map[string]any{"path": x.Path, "mode": x.Mode}
			if x.Host != "" {
				xc["host"] = x.Host
				srv["xhttp_host"] = x.Host
			}
			client["xhttp-opts"] = xc
		} else {
			srv["flow"] = combo.Flow
			client["flow"] = combo.Flow
		}
		uuid := UUID()
		priv, pub, err := RealityKeyPair()
		if err != nil {
			return domain.Node{}, err
		}
		sni := opts.SNI
		if sni == "" {
			sni = DefaultSNI
		}
		sid := ShortID()
		client["uuid"] = uuid

		client["tls"] = true
		client["servername"] = sni
		client["client-fingerprint"] = "chrome"
		client["reality-opts"] = map[string]any{"public-key": pub, "short-id": sid}
		client["network"] = combo.Transport
		client["udp"] = true
		srv["uuid"] = uuid

		srv["reality_private_key"] = priv
		srv["reality_public_key"] = pub
		srv["reality_short_id"] = sid
		srv["handshake_server"] = sni
		srv["handshake_port"] = 443
		delete(srv, "cert_mode")
	case domain.ProtocolAnyTLS:
		pw := Password(16)
		client["password"] = pw
		client["sni"] = tlsDomain
		client["client-fingerprint"] = "chrome"
		client["udp"] = true
		if insecure {
			client["skip-cert-verify"] = true
		}
		srv["password"] = pw
	case domain.ProtocolHysteria2:
		pw := Password(16)
		client["password"] = pw
		client["sni"] = tlsDomain
		client["alpn"] = []string{"h3"}
		if insecure {
			client["skip-cert-verify"] = true
		}
		srv["password"] = pw
		if opts.Obfs {
			op := Password(12)
			client["obfs"] = "salamander"
			client["obfs-password"] = op
			srv["obfs_password"] = op
		}
	case domain.ProtocolTUIC:
		uuid := UUID()
		pw := Password(16)
		client["uuid"] = uuid
		client["password"] = pw
		client["sni"] = tlsDomain
		client["alpn"] = []string{"h3"}
		client["congestion-controller"] = "bbr"
		client["udp-relay-mode"] = "native"
		if insecure {
			client["skip-cert-verify"] = true
		}
		srv["uuid"] = uuid
		srv["password"] = pw
	case domain.ProtocolTrojan:
		if opts.Trojan != nil {
			combo, err := opts.Trojan.Normalized()
			if err != nil {
				return domain.Node{}, err
			}
			setTLSTransport(client, srv, combo.Transport, combo.WS, combo.GRPC)
		}
		pw := Password(16)
		client["password"] = pw
		client["sni"] = tlsDomain
		client["udp"] = true
		if insecure {
			client["skip-cert-verify"] = true
		}
		srv["password"] = pw
	case domain.ProtocolShadowsocks:
		method := opts.SSMethod
		if method == "" {
			method = "2022-blake3-aes-128-gcm"
		}
		var key string
		switch method {
		case "2022-blake3-aes-128-gcm":
			key = SS2022Key(16)
		case "2022-blake3-aes-256-gcm":
			key = SS2022Key(32)
		case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
			key = Password(32)
		default:
			return domain.Node{}, errors.New("不支持的 Shadowsocks 加密方式")
		}
		if opts.SSNetwork != "" && opts.SSNetwork != "both" && opts.SSNetwork != "tcp" {
			return domain.Node{}, errors.New("Shadowsocks 传输只支持 tcp 或 both")
		}
		client["cipher"] = method
		client["password"] = key
		client["udp"] = opts.SSNetwork != "tcp"
		srv["method"] = method
		srv["password"] = key
		if opts.SSNetwork == "tcp" {
			srv["network"] = "tcp"
		}
		delete(srv, "cert_mode")
		delete(srv, "tls_domain")
	case domain.ProtocolWireGuard:
		c, serverConfig, err := wgconfig.Generate(server.IPv4Only)
		if err != nil {
			return domain.Node{}, err
		}
		cb, _ := json.Marshal(c)
		sb, _ := json.Marshal(serverConfig)
		client = map[string]any{}
		srv = map[string]any{}
		_ = json.Unmarshal(cb, &client)
		_ = json.Unmarshal(sb, &srv)
	case domain.ProtocolMieru:
		transport := opts.MieruTransport
		if transport == "" {
			transport = "TCP"
		}
		if transport != "TCP" && transport != "UDP" {
			return domain.Node{}, errors.New("mieru 传输须为 TCP 或 UDP")
		}
		if opts.Port < 1025 {
			return domain.Node{}, errors.New("mita 监听端口必须至少为 1025")
		}
		username, password := "vpsct-"+Password(8), Password(24)
		client = map[string]any{"username": username, "password": password, "transport": transport, "udp": true}
		srv = map[string]any{"username": username, "password": password, "transport": transport, "udp": true}
	case domain.ProtocolSnell:
		psk := Password(24)
		ver := opts.SnellVersion
		if ver == 0 {
			ver = 4
		}
		client["psk"] = psk
		client["version"] = ver
		client["udp"] = true
		srv["psk"] = psk
		srv["version"] = ver
		delete(srv, "cert_mode")
		delete(srv, "tls_domain")
	default:
		return domain.Node{}, fmt.Errorf("协议 %s 不支持自动部署", opts.Protocol)
	}
	cb, _ := json.Marshal(client)
	sb, _ := json.Marshal(srv)
	sid := server.ID
	return domain.Node{
		Network:      network,
		Name:         name,
		Protocol:     opts.Protocol,
		Server:       host,
		Port:         opts.Port,
		Params:       cb,
		ServerParams: sb,
		Source:       domain.NodeDeployed,
		ServerID:     &sid,
		ListenPort:   opts.Port,
		Core:         core,
		Enabled:      true,
		Tags:         []string{},
	}, nil
}

// RegenerateCredentials rotates secrets of an existing deployed node in place
// (used when a share is revoked and re-issued, or on key compromise).
func RegenerateCredentials(n *domain.Node, server domain.Server) error {
	var oldClient map[string]any
	_ = json.Unmarshal(n.Params, &oldClient)
	var oldSrv map[string]any
	_ = json.Unmarshal(n.ServerParams, &oldSrv)
	opts := Options{Name: n.Name, Protocol: n.Protocol, Port: n.ListenPort, CertMode: fmt.Sprint(oldSrv["cert_mode"])}
	if n.Protocol == domain.ProtocolVLESS {
		var err error
		opts.VLESS, err = VLESSFromNode(*n)
		if err != nil {
			return err
		}
		if opts.VLESS.Transport == "xhttp" && n.Network != nil {
			return errors.New("XHTTP 不支持自定义网络策略")
		}
	}
	if n.Protocol == domain.ProtocolTrojan {
		combo, err := TrojanFromNode(*n)
		if err != nil {
			return err
		}
		// An unmarked native deployment keeps its original parameter layout.
		if _, explicit := oldSrv["security"]; explicit {
			opts.Trojan = combo
		}
	}
	if n.Protocol == domain.ProtocolShadowsocks {
		opts.SSMethod, _ = oldSrv["method"].(string)
		opts.SSNetwork, _ = oldSrv["network"].(string)
	}
	if transport, ok := oldSrv["transport"].(string); ok {
		opts.MieruTransport = transport
	}
	if id, ok := oldSrv["cert_id"].(string); ok {
		opts.CertID = id
	}
	if opts.CertMode == "<nil>" {
		opts.CertMode = ""
	}
	if sni, ok := oldSrv["handshake_server"].(string); ok {
		opts.SNI = sni
	}
	if d, ok := oldSrv["tls_domain"].(string); ok && d != n.Server {
		opts.Domain = d
	}
	if _, ok := oldClient["obfs"]; ok {
		opts.Obfs = true
	}
	if v, ok := oldSrv["version"]; ok {
		if f, ok := v.(float64); ok {
			opts.SnellVersion = int(f)
		}
	}
	fresh, err := NewNode(server, n.Server, opts)
	if err != nil {
		return err
	}
	n.Params = fresh.Params
	n.ServerParams = fresh.ServerParams
	n.Core = fresh.Core
	return nil
}
