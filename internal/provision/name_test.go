package provision

import (
	"testing"

	"ctlvps/internal/domain"
)

func TestDefaultNodeName(t *testing.T) {
	got := DefaultNodeName(domain.Server{Name: "Zouter", Region: "hk"}, domain.ProtocolAnyTLS)
	want := "Zouter-HK-AnyTLS"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if DefaultNodeName(domain.Server{Name: "Zouter"}, "vless") != "Zouter-VLESS" {
		t.Fatalf("no region: %q", DefaultNodeName(domain.Server{Name: "Zouter"}, "vless"))
	}
	if DefaultNodeName(domain.Server{Name: "Neburst", Region: "JP"}, "hysteria2") != "Neburst-JP-Hysteria2" {
		t.Fatalf("jp: %q", DefaultNodeName(domain.Server{Name: "Neburst", Region: "JP"}, "hysteria2"))
	}
	if DefaultNodeName(domain.Server{Name: "DMIT", Region: "US"}, "vless") != "DMIT-US-VLESS" {
		t.Fatalf("us: %q", DefaultNodeName(domain.Server{Name: "DMIT", Region: "US"}, "vless"))
	}
}

func TestNewNodeUsesDefaultName(t *testing.T) {
	n, err := NewNode(domain.Server{Name: "Zouter", Region: "HK", CoreMode: domain.CoreModeStable, CertMode: "self_signed", PublicHost: "z.example"}, "z.example", Options{Protocol: domain.ProtocolAnyTLS, Port: 21000})
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "Zouter-HK-AnyTLS" {
		t.Fatalf("name: %q", n.Name)
	}
}
