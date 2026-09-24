package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestOfficialCoreRejectsUDPActivationButAllowsDisabledRecord(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	nodeRequest, diag, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, nodeRequest.NodeID)
	diag.NetworkForwardVersion = agentproto.NetworkForwardVersion
	diag.Cores = []agentproto.CoreStatus{{Name: "sing-box", Version: corecompat.NetworkBaseline, Installed: true}}
	if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: corecompat.NetworkBaseline}); err != nil {
		t.Fatal(err)
	}
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	f := forwardFixture(*n.ServerID, 25101)
	f.Config.TargetHost = "203.0.113.10"
	f.Config.Network, f.Config.MaxTCPConnections, f.Config.MaxUDPSessions, f.Config.UDPIdleSeconds = "udp", 0, 8, 2
	in := PortForwardRequest{ID: strings.Repeat("e", 32), Action: "create", ServerID: f.ServerID, Name: f.Name, Enabled: true, Config: &f.Config}
	v, err := s.PreviewPortForward(ctx, in)
	if err != nil || v.Ready {
		t.Fatal("UDP unexpectedly admitted", v, err)
	}
	found := false
	for _, c := range v.Checks {
		if c.Code == "forward_udp_core" && !c.Ready {
			found = true
		}
	}
	if !found {
		t.Fatal("missing reason for disabled UDP")
	}
	in.Enabled = false
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || !v.Ready {
		t.Fatal("disabled history rejected", v, err)
	}
}

func TestForwardReviewRechecksCapabilityPortsAndImpact(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	nodeRequest, diag, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, nodeRequest.NodeID)
	f := forwardFixture(*n.ServerID, 25101)
	f.Config.TargetHost = "203.0.113.10"
	in := PortForwardRequest{ID: strings.Repeat("e", 32), Action: "create", ServerID: f.ServerID, Name: f.Name, Enabled: true, Config: &f.Config}
	v, err := s.PreviewPortForward(ctx, in)
	if err != nil || v.Ready {
		t.Fatal("old agent admitted forward", v, err)
	}
	diag.NetworkForwardVersion = 1
	writeNetworkDiagnostics(t, s, f.ServerID, diag)
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || !v.Ready || v.Impact == nil || len(v.Impact.Nodes) != 1 {
		t.Fatal(v, err)
	}
	if _, err = s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactRequired) {
		t.Fatal("no preview accepted", err)
	}
	in.ExpectedImpact = v.Impact.Token
	// A same-port node introduced after preview must invalidate its impact and
	// must never leave a half-written resource/operation behind.
	other := n
	other.ID, other.Name, other.ListenPort, other.Network = 0, "new peer", f.Config.ListenPort, nil
	if err = s.CreateNode(ctx, &other); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal("stale review accepted", err)
	}
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || v.Ready {
		t.Fatal("occupied port admitted", v, err)
	}
	if err = s.DeleteNode(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || !v.Ready {
		t.Fatal(v, err)
	}
	in.ExpectedImpact = v.Impact.Token
	op, err := s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	// Retries acknowledge the same committed request even after readiness is lost.
	diag.NetworkForwardVersion = 0
	writeNetworkDiagnostics(t, s, f.ServerID, diag)
	replay, err := s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{})
	if err != nil || replay != op {
		t.Fatal("committed retry re-ran admission", replay, err)
	}
	rows, _ := s.ListPortForwards(ctx, f.ServerID)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
}

func TestPrivateForwardCanBeSavedDisabledButRequiresOwnGrantToEnable(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	nr, diag, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, nr.NodeID)
	diag.NetworkForwardVersion = 1
	diag.ForwardPrivateVersion = 1
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	f := forwardFixture(*n.ServerID, 25102)
	f.Config.TargetHost = "10.42.0.2"
	in := PortForwardRequest{ID: strings.Repeat("b", 32), Action: "create", ServerID: f.ServerID, Name: f.Name, Enabled: false, Config: &f.Config}
	v, err := s.PreviewPortForward(ctx, in)
	if err != nil || !v.Ready {
		t.Fatal(v, err)
	}
	in.ExpectedImpact = v.Impact.Token
	op, err := s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	in.ID = strings.Repeat("c", 32)
	in.Action = "update"
	in.ServerID = 0
	in.ForwardID = op.ResourceID
	in.ExpectedRevision = 1
	in.Enabled = true
	in.ExpectedImpact = ""
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || v.Ready {
		t.Fatal("missing grant", v, err)
	}
	g := networkconfig.ForwardGrant{ForwardID: op.ResourceID, EgressProfileID: f.Config.EgressProfileID, Address: "10.42.0.2", Port: f.Config.TargetPort}
	diag.ForwardGrants = []networkconfig.ForwardGrant{g}
	writeNetworkDiagnostics(t, s, f.ServerID, diag)
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || !v.Ready {
		t.Fatal(v, err)
	}
	in.ExpectedImpact = v.Impact.Token
	diag.ForwardGrants = nil
	writeNetworkDiagnostics(t, s, f.ServerID, diag)
	if _, err = s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
		t.Fatal("revoked grant accepted on submit", err)
	}
}
