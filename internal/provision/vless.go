package provision

import (
	"encoding/json"
	"errors"

	"ctlvps/internal/domain"
	"ctlvps/internal/nodecombo"
)

// VLESSFromNode reconstructs a deployment choice without exposing key material.
// Old Vision nodes have no explicit server-side network field. Never infer TCP
// from a missing field on a different core or with a contradictory client profile.
func VLESSFromNode(n domain.Node) (*nodecombo.VLESS, error) {
	invalid := errors.New("无法无损恢复节点的 VLESS 组合")
	if n.Protocol != domain.ProtocolVLESS {
		return nil, invalid
	}
	if t, tls, err := recoverTLSProfile(n); err != nil {
		return nil, err
	} else if tls {
		return &nodecombo.VLESS{Transport: t.Transport, Security: t.Security, WS: t.WS, GRPC: t.GRPC}, nil
	}
	var srv struct {
		Network string `json:"network"`
		Flow    string `json:"flow"`
		Path    string `json:"xhttp_path"`
		Mode    string `json:"xhttp_mode"`
		Host    string `json:"xhttp_host"`
	}
	var client struct {
		Network string           `json:"network"`
		Flow    string           `json:"flow"`
		XHTTP   *nodecombo.XHTTP `json:"xhttp-opts"`
	}
	if json.Unmarshal(n.ServerParams, &srv) != nil || json.Unmarshal(n.Params, &client) != nil {
		return nil, invalid
	}
	v := nodecombo.VLESS{Transport: srv.Network, Security: "reality", Flow: srv.Flow}
	if srv.Network == "xhttp" {
		if n.Core != domain.CoreXray || client.XHTTP == nil || srv.Path == "" || srv.Mode == "" {
			return nil, invalid
		}
		x := nodecombo.XHTTP{Path: srv.Path, Mode: srv.Mode, Host: srv.Host}
		if *client.XHTTP != x {
			return nil, invalid
		}
		v.XHTTP = &x
	} else if n.Core != domain.CoreSingBox || srv.Flow != nodecombo.Vision || client.XHTTP != nil || srv.Path != "" || srv.Mode != "" || srv.Host != "" {
		return nil, invalid
	}
	normalized, err := v.Normalized()
	if err != nil {
		return nil, err
	}
	if normalized.Flow != client.Flow || normalized.Transport != client.Network {
		return nil, invalid
	}
	return &normalized, nil
}
