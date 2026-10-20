package subscription

import (
	"context"
	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
	"encoding/json"
	"fmt"
	"strings"
)

// AccessRouteNode projects an independent entry address over an acknowledged
// landing configuration. It never edits or clones the landing credentials.
func (s *Service) AccessRouteNode(ctx context.Context, id string) (domain.Node, bool, error) {
	r, n, usable, err := s.Store.AccessRouteLanding(ctx, id)
	if err != nil || !usable {
		return n, false, err
	}
	// The dial endpoint changes, while the TLS and HTTP identities still belong
	// to the original service. Make implicit hostname defaults explicit first.
	p := proxynode.FromDomain(n)
	key := "sni"
	if n.Protocol == domain.ProtocolVLESS || n.Protocol == domain.ProtocolVMess {
		key = "servername"
	}
	switch n.Protocol {
	case domain.ProtocolVLESS, domain.ProtocolVMess, domain.ProtocolTrojan, domain.ProtocolAnyTLS, domain.ProtocolHysteria2, domain.ProtocolTUIC, domain.ProtocolHTTP, domain.ProtocolSocks5:
		if p.Str(key) == "" {
			p.Params[key] = n.Server
		}
	}
	switch p.Str("network") {
	case "ws", "httpupgrade":
		opts := p.Sub("ws-opts")
		if opts == nil {
			opts = map[string]any{}
		}
		headers, _ := opts["headers"].(map[string]any)
		if headers == nil {
			headers = map[string]any{}
		}
		hasHost := false
		if value, ok := headers["Host"]; ok && fmt.Sprint(value) != "" {
			hasHost = true
		}
		for name, value := range headers {
			if !hasHost && strings.EqualFold(name, "Host") && fmt.Sprint(value) != "" {
				headers["Host"] = value
				hasHost = true
			}
		}
		if !hasHost {
			headers["Host"] = n.Server
		}
		opts["headers"] = headers
		p.Params["ws-opts"] = opts
	case "h2", "http":
		k := "h2-opts"
		if p.Str("network") == "http" {
			k = "http-opts"
		}
		opts := p.Sub(k)
		if opts == nil {
			opts = map[string]any{}
		}
		if value, ok := opts["host"]; !ok || value == nil {
			opts["host"] = []string{n.Server}
		}
		p.Params[k] = opts
	}
	n.Params, _ = json.Marshal(p.Params)
	n.ID = 0
	n.Name = r.Name
	n.Server = r.Request.AdvertiseHost
	n.Port = r.Request.EntryPort
	n.ServerID = nil
	n.ListenPort = 0
	n.Network = nil
	n.NetworkRevision = 0
	n.ServerParams = nil
	// The route transport can deliberately provide only TCP even when C also
	// supports raw UDP. VLESS/Trojan business UDP stays inside TCP and must
	// remain available. SOCKS5 UDP-associate uses dynamic ports and is excluded.
	if (r.Request.Network == "tcp" && n.Protocol == domain.ProtocolShadowsocks) || n.Protocol == domain.ProtocolSocks5 {
		p := map[string]any{}
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return n, false, err
		}
		p["udp"] = false
		n.Params, _ = json.Marshal(p)
	}
	return n, true, nil
}
