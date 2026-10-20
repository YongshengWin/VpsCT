package natconfig

import (
	"errors"
	"fmt"
	"sort"
)

// ProtocolNetworks defines the transports required by an inbound. Keep this
// shared by admission, allocation, and the host's nft translation rules.
func ProtocolNetworks(protocol string, params map[string]any) ([]string, error) {
	switch protocol {
	case "ss", "shadowsocks":
		if network, exists := params["network"]; exists {
			value, ok := network.(string)
			if !ok {
				return nil, errors.New("Shadowsocks 传输无效")
			}
			switch value {
			case "tcp":
				return []string{"tcp"}, nil
			case "", "both":
			default:
				return nil, errors.New("Shadowsocks 传输只支持 tcp 或 both")
			}
		}
		return []string{"tcp", "udp"}, nil
	case "hysteria2", "tuic", "wireguard":
		return []string{"udp"}, nil
	case "vless", "vmess", "trojan", "anytls", "snell":
		return []string{"tcp"}, nil
	case "mieru":
		transport, _ := params["transport"].(string)
		if transport == "TCP" {
			return []string{"tcp"}, nil
		}
		if transport == "UDP" {
			return []string{"udp"}, nil
		}
		return nil, errors.New("NAT 节点的 mieru 传输无效")
	default:
		return nil, fmt.Errorf("无法确定节点协议 %q 的 NAT 入站传输", protocol)
	}
}

// Pools returns sorted public ports for TCP, UDP, and their intersection.
// Identity mappings are usable ports too, although nft need not translate them.
func (c Config) Pools() (map[string][]int, error) {
	pairs, err := c.Expand()
	if err != nil {
		return nil, err
	}
	sets := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, p := range pairs {
		sets[p.Network][p.PublicPort] = true
	}
	out := map[string][]int{"tcp": {}, "udp": {}, "both": {}}
	for network, ports := range sets {
		for port := range ports {
			out[network] = append(out[network], port)
		}
		sort.Ints(out[network])
	}
	for _, port := range out["tcp"] {
		if sets["udp"][port] {
			out["both"] = append(out["both"], port)
		}
	}
	return out, nil
}

// SupportsPort requires every requested transport to be allocated by the
// provider. A TCP allocation cannot qualify an inbound which also needs UDP.
func (c Config) SupportsPort(publicPort int, networks []string) (bool, error) {
	if publicPort < 1 || publicPort > 65535 || len(networks) == 0 {
		return false, errors.New("NAT 端口或传输要求无效")
	}
	for _, network := range networks {
		if network != "tcp" && network != "udp" {
			return false, errors.New("NAT 传输要求只支持 tcp 或 udp")
		}
	}
	pools, err := c.Pools()
	if err != nil {
		return false, err
	}
	for _, network := range networks {
		ports := pools[network]
		i := sort.SearchInts(ports, publicPort)
		if i == len(ports) || ports[i] != publicPort {
			return false, nil
		}
	}
	return true, nil
}
