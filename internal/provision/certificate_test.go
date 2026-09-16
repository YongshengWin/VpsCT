package provision

import (
	"ctlvps/internal/domain"
	"encoding/json"
	"testing"
)

func TestExternalCertificateRequiresLocalID(t *testing.T) {
	s := domain.Server{CertMode: "external", PublicHost: "node.example", CoreMode: domain.CoreModeStable}
	for _, id := range []string{"", "/etc/secret", "../secret"} {
		if _, e := NewNode(s, "", Options{Protocol: "trojan", Port: 21001, CertID: id}); e == nil {
			t.Fatalf("accepted path %q", id)
		}
	}
	n, e := NewNode(s, "", Options{Protocol: "trojan", Port: 21001, CertID: "web-cert"})
	if e != nil {
		t.Fatal(e)
	}
	if e = RegenerateCredentials(&n, s); e != nil {
		t.Fatal(e)
	}
	var p map[string]any
	json.Unmarshal(n.ServerParams, &p)
	if p["cert_id"] != "web-cert" {
		t.Fatal("credential rotation lost certificate binding")
	}
}
