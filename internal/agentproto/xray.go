package agentproto

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"regexp"

	"ctlvps/internal/domain"

	"ctlvps/internal/nodecombo"
)

const XrayVersion = nodecombo.XrayVersion
const XrayHeader = nodecombo.XrayHeader

func PinnedXray() CoreVersion {
	return CoreVersion{Version: domain.DefaultXrayVersion, URL: domain.DefaultXrayURL, SHA256: map[string]string{"amd64": domain.DefaultXraySHA256}}
}
func OfficialXrayPin(v CoreVersion) bool {
	return v.Version == domain.DefaultXrayVersion && v.URL == domain.DefaultXrayURL && len(v.SHA256) == 1 && v.SHA256["amd64"] == domain.DefaultXraySHA256
}

var xrayUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Xray v1 deliberately has no general JSON, destination, socket path or plugin input.
// Keys are validated without returning any portion of secret-bearing parameters.
func ValidateXrayNode(ds *DesiredState, n NodeSpec) error {
	if ds == nil || ds.XrayVersion != XrayVersion || ds.CoreMode != "stable" || ds.NAT != nil || n.Core != "xray" || n.Protocol != "vless" || n.Cert != nil || n.Network != nil || n.RuntimeNetwork != nil || n.AllowPrivate || n.ListenPort < 1 || n.ListenPort > 65535 || n.NodeID <= 0 {
		return errors.New("Xray XHTTP requires stable VLESS without NAT, certificate, private access or network bindings")
	}
	if !OfficialXrayPin(ds.Versions["xray"]) {
		return errors.New("Xray requires the reviewed official AMD64 release pin")
	}
	for k, v := range n.Params {
		switch k {
		case "handshake_port":
			switch p := v.(type) {
			case int:
				if p != 443 {
					return errors.New("Xray requires REALITY handshake port 443")
				}
			case float64:
				if p != 443 {
					return errors.New("Xray requires REALITY handshake port 443")
				}
			default:
				return errors.New("invalid REALITY handshake port")
			}
		case "network", "xhttp_path", "xhttp_host", "xhttp_mode", "uuid", "reality_private_key", "reality_public_key", "reality_short_id", "handshake_server", "tls_domain", "flow":
			if _, ok := v.(string); !ok {
				return errors.New("invalid Xray parameter type")
			}
		default:
			return errors.New("unsupported Xray parameter")
		}
	}
	s := func(k string) string { v, _ := n.Params[k].(string); return v }
	if s("network") != "xhttp" || s("flow") != "" || s("xhttp_path") == "" || s("xhttp_mode") == "" {
		return errors.New("Xray requires an explicit XHTTP path and mode without Vision")
	}
	combo := nodecombo.VLESS{Transport: "xhttp", Security: "reality", XHTTP: &nodecombo.XHTTP{Path: s("xhttp_path"), Host: s("xhttp_host"), Mode: s("xhttp_mode")}}
	if norm, err := combo.Normalized(); err != nil || norm.XHTTP == nil || *norm.XHTTP != *combo.XHTTP {
		return errors.New("invalid or noncanonical XHTTP options")
	}
	host := s("handshake_server")

	if !nodecombo.ValidDNSName(host) {
		return errors.New("invalid REALITY handshake domain")
	}
	if !xrayUUID.MatchString(s("uuid")) {
		return errors.New("invalid Xray client identity")
	}
	id, err := hex.DecodeString(s("reality_short_id"))
	if err != nil || len(id) < 1 || len(id) > 8 {
		return errors.New("invalid REALITY short ID")
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(s("reality_private_key"))
	if err != nil || len(key) != 32 {
		return errors.New("invalid REALITY key")
	}
	priv, err := ecdh.X25519().NewPrivateKey(key)
	if err != nil || base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()) != s("reality_public_key") {
		return errors.New("inconsistent REALITY keys")
	}
	return nil
}
