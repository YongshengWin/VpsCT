package provision

import (
	"encoding/json"
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/nodecombo"
)

func TestVLESSRotationPreservesTransportAndDistinctNames(t *testing.T) {
	s := domain.Server{ID: 7, CoreMode: domain.CoreModeStable, PublicHost: "access.example.org"}
	for _, v := range []*nodecombo.VLESS{nil, {Transport: "xhttp", XHTTP: &nodecombo.XHTTP{Path: "/entry", Host: "http.example.org", Mode: "stream-up"}}} {
		n, err := NewNode(s, "", Options{Protocol: "vless", Port: 41000, SNI: "handshake.example.org", VLESS: v})
		if err != nil {
			t.Fatal(err)
		}
		var original map[string]any
		_ = json.Unmarshal(n.Params, &original)
		oldCore := n.Core
		if err = RegenerateCredentials(&n, s); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		_ = json.Unmarshal(n.Params, &got)
		if got["uuid"] == original["uuid"] || got["servername"] != "handshake.example.org" || n.Server != "access.example.org" || n.ListenPort != 41000 || n.Core != oldCore || strings.Contains(string(n.Params), "private") {
			t.Fatal("rotation changed endpoint/choice or reused credentials")
		}
		current, err := VLESSFromNode(n)
		if err != nil {
			t.Fatal(err)
		}
		if v != nil {
			if current.Transport != "xhttp" || *current.XHTTP != *v.XHTTP || got["flow"] != nil {
				t.Fatal("rotation changed XHTTP")
			}
			n.Core = domain.CoreSingBox
			if RegenerateCredentials(&n, s) == nil {
				t.Fatal("inconsistent core silently treated as Vision")
			}
		} else if current.Flow != nodecombo.Vision || current.Transport != "tcp" || oldCore != domain.CoreSingBox {
			t.Fatal("legacy TCP profile changed")
		}
	}
}

func TestXHTTPProvisionRejectsHostAndNetworkConflicts(t *testing.T) {
	for _, tc := range []struct {
		s   domain.Server
		n   *networkconfig.Node
		sni string
	}{{domain.Server{CoreMode: domain.CoreModeLean}, nil, ""}, {domain.Server{CoreMode: domain.CoreModeStable, IsNAT: true}, nil, ""}, {domain.Server{CoreMode: domain.CoreModeStable}, &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}, ""}, {domain.Server{CoreMode: domain.CoreModeStable}, nil, "service.localhost"}} {
		if _, err := NewNode(tc.s, "", Options{Protocol: "vless", Port: 40001, VLESS: &nodecombo.VLESS{Transport: "xhttp"}, Network: tc.n, SNI: tc.sni}); err == nil {
			t.Fatal("XHTTP admitted unsupported host or network")
		}
	}
}
