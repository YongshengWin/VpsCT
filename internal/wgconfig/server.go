package wgconfig

import (
	"crypto/ecdh"
	"crypto/rand"
	"ctlvps/internal/networkconfig"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
)

// Server is one independently metered single-peer user-mode endpoint. It does
// not create host interfaces, routes or forwarding rules.
type Server struct {
	PrivateKey   string   `json:"private_key"`
	PeerKey      string   `json:"peer_public_key"`
	PresharedKey string   `json:"pre_shared_key"`
	Address      []string `json:"address"`
	PeerAddress  []string `json:"peer_address"`
	MTU          int      `json:"mtu"`
}

func Generate(ipv4Only bool) (Client, Server, error) {
	var c Client
	var s Server
	server, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return c, s, err
	}
	peer, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return c, s, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return c, s, err
	}
	encode := base64.StdEncoding.EncodeToString
	c = Client{IP: "10.203.0.2", PrivateKey: encode(peer.Bytes()), PublicKey: encode(server.PublicKey().Bytes()), PresharedKey: encode(key), AllowedIPs: []string{"0.0.0.0/0"}, MTU: 1408, UDP: true, Keepalive: 25, DNS: []string{"1.1.1.1"}}
	s = Server{PrivateKey: encode(server.Bytes()), PeerKey: encode(peer.PublicKey().Bytes()), PresharedKey: encode(key), Address: []string{"10.203.0.1/32"}, PeerAddress: []string{"10.203.0.2/32"}, MTU: 1408}
	if !ipv4Only {
		c.IPv6 = "fd73:6374::2"
		c.AllowedIPs = append(c.AllowedIPs, "::/0")
		s.Address = append(s.Address, "fd73:6374::1/128")
		s.PeerAddress = append(s.PeerAddress, "fd73:6374::2/128")
	}
	return c, s, nil
}
func DecodeServer(params map[string]any) (Server, error) {
	var s Server
	for k := range params {
		switch k {
		case "private_key", "peer_public_key", "pre_shared_key", "address", "peer_address", "mtu":
		default:
			return s, errors.New("WireGuard 服务端字段不受支持")
		}
	}
	b, err := json.Marshal(params)
	if err != nil || len(b) > 8192 {
		return s, errors.New("WireGuard 服务端配置无效")
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.MTU < 1280 || s.MTU > 9000 || len(s.Address) < 1 || len(s.Address) > 2 || len(s.PeerAddress) != len(s.Address) {
		return s, errors.New("WireGuard 服务端地址或 MTU 无效")
	}
	private, err := networkconfig.WireGuardKey(s.PrivateKey)
	if err != nil {
		return s, err
	}
	public, err := networkconfig.WireGuardKey(s.PeerKey)
	if err != nil {
		return s, err
	}
	if _, err = networkconfig.WireGuardKey(s.PresharedKey); err != nil {
		return s, err
	}
	pk, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return s, err
	}
	peer, err := ecdh.X25519().NewPublicKey(public)
	if err != nil {
		return s, err
	}
	if _, err = pk.ECDH(peer); err != nil {
		return s, errors.New("WireGuard Peer 公钥无效")
	}
	families := map[bool]bool{}
	for i, raw := range s.Address {
		a, e := netip.ParsePrefix(raw)
		p, e2 := netip.ParsePrefix(s.PeerAddress[i])
		if e != nil || e2 != nil || a.Bits() != a.Addr().BitLen() || p.Bits() != p.Addr().BitLen() || a.Addr().Is4() != p.Addr().Is4() || a == p || !a.Addr().IsGlobalUnicast() || !p.Addr().IsGlobalUnicast() || a.Addr().Is4In6() || p.Addr().Is4In6() || families[a.Addr().Is4()] {
			return s, errors.New("WireGuard 服务端只接受每个地址族一对独立主机地址")
		}
		families[a.Addr().Is4()] = true
	}
	return s, nil
}
func (s Server) Endpoint(tag string, port int) map[string]any {
	return map[string]any{"type": "wireguard", "tag": tag, "system": false, "listen_port": port, "address": s.Address, "private_key": s.PrivateKey, "mtu": s.MTU, "workers": 1, "peers": []any{map[string]any{"public_key": s.PeerKey, "pre_shared_key": s.PresharedKey, "allowed_ips": s.PeerAddress}}}
}
