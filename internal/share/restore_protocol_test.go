package share

import (
	"encoding/json"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

func TestLegacyProtocolShareReissue(t *testing.T) {
	m, st, srv, _ := setup(t)
	ctx := t.Context()
	sh := domain.Share{Name: "fixture", Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"snell", "ss"}}}}
	if _, err := m.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil || len(nodes) != 2 {
		t.Fatalf("initial nodes = %d, err = %v", len(nodes), err)
	}
	if err := m.Revoke(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reissue(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	for _, old := range nodes {
		n, err := st.GetNode(ctx, old.ID)
		if err != nil || !n.Enabled || n.Revoked || n.ListenPort != old.ListenPort {
			t.Fatalf("restore %s on original port: %v", old.Protocol, err)
		}
		var before, after map[string]any
		if err := json.Unmarshal(old.Params, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(n.Params, &after); err != nil {
			t.Fatal(err)
		}
		switch old.Protocol {
		case domain.ProtocolSnell:
			if after["version"] != float64(4) || after["psk"] == before["psk"] {
				t.Fatal("Snell restore changed the legacy default or retained the key")
			}
		case domain.ProtocolShadowsocks:
			if after["udp"] != true || after["method"] != before["method"] || after["password"] == before["password"] {
				t.Fatal("SS restore changed the legacy transport or cipher, or retained the key")
			}
		}
	}
}
