package provision

import (
	"ctlvps/internal/domain"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSSOptionsAndRotation(t *testing.T) {
	server := domain.Server{ID: 1, PublicHost: "192.0.2.10"}
	for _, method := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"} {
		for _, network := range []string{"tcp", "both"} {
			n, err := NewNode(server, "", Options{Protocol: "ss", Port: 30001, SSMethod: method, SSNetwork: network})
			if err != nil {
				t.Fatal(err)
			}
			var client, srv map[string]any
			json.Unmarshal(n.Params, &client)
			json.Unmarshal(n.ServerParams, &srv)
			if client["cipher"] != method || srv["method"] != method || client["udp"] != (network != "tcp") {
				t.Fatal("SS options lost")
			}
			if network == "tcp" && srv["network"] != "tcp" {
				t.Fatal("TCP listener not restricted")
			}
			if method[:4] == "2022" {
				key, err := base64.StdEncoding.DecodeString(srv["password"].(string))
				want := 16
				if method == "2022-blake3-aes-256-gcm" {
					want = 32
				}
				if err != nil || len(key) != want {
					t.Fatal("invalid SS2022 key length")
				}
			}
			old := srv["password"]
			if err := RegenerateCredentials(&n, server); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(n.ServerParams, &srv)
			if srv["method"] != method || srv["password"] == old || (network == "tcp" && srv["network"] != "tcp") {
				t.Fatal("rotation changed options or retained secret")
			}
		}
	}
	for _, opts := range []Options{{SSMethod: "none"}, {SSNetwork: "udp"}} {
		opts.Protocol = "ss"
		opts.Port = 30001
		if _, err := NewNode(server, "", opts); err == nil {
			t.Fatal("invalid SS options accepted")
		}
	}
}
