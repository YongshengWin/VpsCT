package proxynode

import (
	"ctlvps/internal/wgconfig"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// parseWireGuard reads one peer and rejects all hooks, file paths, routing and
// multi-peer directives instead of partially importing a different tunnel.
func parseWireGuard(text string) ParseResult {
	res := ParseResult{Format: "wireguard"}
	fail := func() ParseResult {
		res.Errors = []string{"WireGuard 配置无效：只接受单 Peer 的地址、密钥、DNS、MTU、端点、允许网段和保活字段，不执行脚本"}
		return res
	}
	if len(text) > 16384 {
		return fail()
	}
	p := Proxy{Name: "WireGuard", Type: "wireguard", Params: map[string]any{"udp": true}}
	section := ""
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if line != "[Interface]" && line != "[Peer]" || seen[line] {
				return fail()
			}
			seen[line] = true
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || section == "" || seen[section+key] {
			return fail()
		}
		seen[section+key] = true
		list := func() []string {
			var v []string
			for _, s := range strings.Split(value, ",") {
				v = append(v, strings.TrimSpace(s))
			}
			return v
		}
		switch section + key {
		case "[Interface]PrivateKey":
			p.Params["private-key"] = value
		case "[Interface]Address":
			for _, raw := range list() {
				prefix, e := netip.ParsePrefix(raw)
				if e != nil {
					return fail()
				}
				field := "ipv6"
				if prefix.Addr().Is4() {
					field = "ip"
				}
				if _, exists := p.Params[field]; exists {
					return fail()
				}
				p.Params[field] = prefix.Addr().String()
			}
		case "[Interface]DNS":
			p.Params["dns"] = list()
			p.Params["remote-dns-resolve"] = true
		case "[Interface]MTU", "[Peer]PersistentKeepalive":
			v, e := strconv.Atoi(value)
			if e != nil {
				return fail()
			}
			field := "mtu"
			if key == "PersistentKeepalive" {
				field = "persistent-keepalive"
			}
			p.Params[field] = v
		case "[Peer]PublicKey":
			p.Params["public-key"] = value
		case "[Peer]PresharedKey":
			p.Params["pre-shared-key"] = value
		case "[Peer]AllowedIPs":
			p.Params["allowed-ips"] = list()
		case "[Peer]Endpoint":
			host, port, e := net.SplitHostPort(value)
			if e != nil {
				return fail()
			}
			p.Server = host
			p.Port, e = strconv.Atoi(port)
			if e != nil {
				return fail()
			}
		default:
			return fail()
		}
	}
	if !seen["[Interface]"] || !seen["[Peer]"] || p.Server == "" || p.Port < 1 {
		return fail()
	}
	c, err := wgconfig.Decode(p.Params)
	if err != nil {
		return fail()
	}
	p.Params = c.Params()
	p.Name = fmt.Sprintf("WireGuard-%s:%d", p.Server, p.Port)
	if validateProxy(p) != nil {
		return fail()
	}
	res.Proxies = []Proxy{p}
	return res
}
