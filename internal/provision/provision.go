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
	"regexp"
	"strings"

	"golang.org/x/crypto/curve25519"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/wgconfig"
)

// Options tune node creation.
type Options struct {
	Network        *networkconfig.Node
	AdvertiseHost  string
	Name           string
	Protocol       string
	Port           int    // 0 = allocate
	SNI            string // reality handshake target / TLS server name
	Domain         string // TLS domain when cert mode is acme/external; empty = public host
	Obfs           bool   // hysteria2 salamander
	SnellVersion   int
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
	client := map[string]any{}
	srv := map[string]any{"cert_mode": certMode, "tls_domain": tlsDomain}
	if certMode == "external" && (opts.Protocol == "anytls" || opts.Protocol == "hysteria2" || opts.Protocol == "tuic" || opts.Protocol == "trojan") {
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`).MatchString(opts.CertID) {
			return domain.Node{}, errors.New("外部证书需要本机策略中登记的证书 ID")
		}
		srv["cert_id"] = opts.CertID
	}
	insecure := certMode == "self_signed"
	switch opts.Protocol {
	case domain.ProtocolVLESS:
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
		client["flow"] = "xtls-rprx-vision"
		client["tls"] = true
		client["servername"] = sni
		client["client-fingerprint"] = "chrome"
		client["reality-opts"] = map[string]any{"public-key": pub, "short-id": sid}
		client["network"] = "tcp"
		client["udp"] = true
		srv["uuid"] = uuid
		srv["flow"] = "xtls-rprx-vision"
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
		pw := Password(16)
		client["password"] = pw
		client["sni"] = tlsDomain
		client["udp"] = true
		if insecure {
			client["skip-cert-verify"] = true
		}
		srv["password"] = pw
	case domain.ProtocolShadowsocks:
		key := SS2022Key(16)
		client["cipher"] = "2022-blake3-aes-128-gcm"
		client["password"] = key
		client["udp"] = true
		srv["method"] = "2022-blake3-aes-128-gcm"
		srv["password"] = key
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
		Core:         domain.CoreFor(opts.Protocol, server.CoreMode),
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
