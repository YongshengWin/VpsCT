package core

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
)

func TestSingBoxGeneratedALPNHandshake(t *testing.T) {
	dir := t.TempDir()
	files, err := EnsureSelfSigned(dir, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(files.Cert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("fixture certificate is invalid")
	}
	d := NewSingBox(Paths{DataDir: dir, LogDir: dir}, NewSystemd())
	d.CertResolver = func(*agentproto.CertSpec) (CertFiles, error) { return files, nil }
	for _, protocol := range []string{"anytls", "trojan"} {
		for _, version := range []string{"1.12.14", corecompat.NetworkBaseline} {
			for _, mode := range []string{"acme", "self_signed", "external"} {
				n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: protocol, ListenPort: 20443, Params: map[string]any{"password": "fixture-only"}, Cert: &agentproto.CertSpec{Mode: mode, Domain: "example.com"}}
				cfg, err := d.BuildConfig(&agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: version}}}, []agentproto.NodeSpec{n})
				if err != nil {
					t.Fatal(err)
				}
				block := cfg["inbounds"].([]any)[0].(map[string]any)["tls"].(map[string]any)
				serverALPN, _ := block["alpn"].([]string)
				// Reproduce the official TLS layer's merge, with a trusted local
				// certificate so this regression never contacts an ACME authority.
				// The integration suite verifies actual issuance and renewal.
				if mode == "acme" {
					if corecompat.ModernConfig(version) {
						serverALPN = append([]string{"acme-tls/1"}, serverALPN...)
					} else {
						serverALPN = append(append([]string{}, serverALPN...), "acme-tls/1")
					}
				}
				for _, tc := range []struct {
					name       string
					alpn       []string
					want       string
					acmeReject bool
				}{
					{"http", []string{"h2", "http/1.1"}, "h2", false},
					{"http1", []string{"http/1.1"}, "http/1.1", false},
					{"none", nil, "", false},
					{"unsupported", []string{"fixture-unsupported"}, "", true},
				} {
					t.Run(protocol+"/"+version+"/"+mode+"/"+tc.name, func(t *testing.T) {
						want := tc.want
						if mode != "acme" {
							want = ""
						}
						server, client := net.Pipe()
						defer server.Close()
						defer client.Close()
						deadline := time.Now().Add(3 * time.Second)
						_ = server.SetDeadline(deadline)
						_ = client.SetDeadline(deadline)
						result := make(chan error, 1)
						go func() {
							result <- tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: serverALPN}).Handshake()
						}()
						conn := tls.Client(client, &tls.Config{RootCAs: roots, ServerName: "example.com", NextProtos: tc.alpn})
						err := conn.Handshake()
						_ = client.Close()
						serverErr := <-result
						if mode == "acme" && tc.acmeReject {
							if err == nil || serverErr == nil {
								t.Fatal("unsupported application protocol unexpectedly accepted")
							}
							return
						}
						if err != nil || serverErr != nil {
							t.Fatalf("TLS handshake: client=%v server=%v", err, serverErr)
						}
						if got := conn.ConnectionState().NegotiatedProtocol; got != want {
							t.Fatalf("negotiated %q, want %q", got, want)
						}
					})
				}
			}
		}
	}
}
