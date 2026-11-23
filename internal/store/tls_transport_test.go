package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestTLSTransportNodeTransactionRejectsStaleHostAndBindings(t *testing.T) {
	ctx := t.Context()
	s := openTest(t)
	sv, nodes := applicationNodes(t, s)
	old := nodes[0]
	n := old
	n.Protocol = "trojan"
	n.ServerParams = json.RawMessage(`{"network":"grpc","security":"tls","grpc_service_name":"service","password":"fixture","cert_mode":"self_signed","tls_domain":"origin.example.org"}`)
	err := s.UpdateNode(ctx, &n)
	if !errors.Is(err, ErrTLSTransportUnavailable) {
		t.Fatalf("stale node update accepted: %v", err)
	}
	if floor, err := s.TLSTransportVersion(ctx, sv.ID); err != nil || floor != 0 {
		t.Fatal("failed transaction changed floor", err)
	}
	_, err = s.db.Exec(`UPDATE agents SET token_hash='fixture',last_seen_at=?,metrics='{"arch":"amd64"}',diagnostics='{"tls_transport_version":1,"security_policy":true,"nftables":true}' WHERE server_id=?`, fmtTime(s.Now()), sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	bad := n
	bad.ServerParams = json.RawMessage(`{"network":"grpc","security":"tls","grpc_service_name":"a/b","password":"fixture","cert_mode":"self_signed","tls_domain":"origin.example.org"}`)
	if err = s.UpdateNode(ctx, &bad); !errors.Is(err, ErrTLSTransportUnavailable) {
		t.Fatal("invalid gRPC service accepted", err)
	}
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
	bad = n
	bad.Network = policy
	bad.ListenPort++
	if err = s.CreateNode(ctx, &bad); !errors.Is(err, ErrTLSTransportUnavailable) {
		t.Fatal("new bound transport accepted", err)
	}
	if err = s.UpdateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, policy, nil); !errors.Is(err, ErrTLSTransportUnavailable) {
		t.Fatal("existing TLS node acquired binding", err)
	}
	if _, err = s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE server_id=?`, fmtTime(s.Now().Add(-time.Hour)), sv.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateNodeCredentials(ctx, &n); !errors.Is(err, ErrTLSTransportUnavailable) {
		t.Fatal("stale host reissued credentials", err)
	}
	if err = s.DeleteNode(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if floor, err := s.TLSTransportVersion(ctx, sv.ID); err != nil || floor != agentproto.TLSTransportVersion {
		t.Fatal("deletion lost cleanup floor", err)
	}
}

func TestTLSTransportBlockReason(t *testing.T) {
	now := time.Now()
	d := agentproto.Diagnostics{TLSTransportVersion: 1, SecurityPolicy: true, Nftables: true}
	a := domain.Agent{TokenHash: "fixture", LastSeenAt: &now, Metrics: json.RawMessage(`{"arch":"amd64"}`)}
	a.Diagnostics, _ = json.Marshal(d)
	if TLSTransportBlockReason(domain.Server{}, a, now) != "" {
		t.Fatal("capable host blocked")
	}
	for _, change := range []func(*agentproto.Diagnostics){func(d *agentproto.Diagnostics) { d.TLSTransportVersion = 0 }, func(d *agentproto.Diagnostics) { d.SecurityPaused = true }, func(d *agentproto.Diagnostics) { d.SecurityPolicy = false }, func(d *agentproto.Diagnostics) { d.Nftables = false }, func(d *agentproto.Diagnostics) { d.MeteringError = "failed" }} {
		x := d
		change(&x)
		a.Diagnostics, _ = json.Marshal(x)
		if TLSTransportBlockReason(domain.Server{}, a, now) == "" {
			t.Fatal("unready host admitted")
		}
	}
}
