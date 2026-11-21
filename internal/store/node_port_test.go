package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"ctlvps/internal/domain"
)

func TestNodePortChangeIsReviewedAtomicAndIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	before, _ := s.GetNode(ctx, in.NodeID)
	port := 34567
	in.ListenPort = &port
	view, err := s.ReviewNodeNetwork(ctx, before.ID, in.Network, nil, &port)
	if err != nil || !view.Ready {
		t.Fatal(view, err)
	}
	in.ExpectedImpact = view.Impact.Token
	if view.Impact.BeforePort != before.ListenPort || view.Impact.AfterPort != port {
		t.Fatal(view.Impact)
	}
	// Changing just the port invalidates a confirmation for another draft.
	other := 34568
	altered := in
	altered.ListenPort = &other
	if _, err = s.RequestReviewedNodeNetwork(ctx, altered, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal(err)
	}
	op, err := s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || op.Status != "queued" {
		t.Fatal(op, err)
	}
	after, err := s.GetNode(ctx, before.ID)
	if err != nil || after.Port != port || after.ListenPort != port || after.ID != before.ID || !bytes.Equal(after.Params, before.Params) || !bytes.Equal(after.ServerParams, before.ServerParams) {
		t.Fatal("identity or credentials changed", err)
	}
	if _, usable, err := s.AppliedSubscriptionNode(ctx, after); err != nil || usable {
		t.Fatal("pending port was published as applied", err)
	}
	replay, err := s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || replay.ID != op.ID || replay.ResourceRevision != op.ResourceRevision {
		t.Fatal(replay, err)
	}
	altered.ExpectedImpact = in.ExpectedImpact
	if _, err = s.RequestReviewedNodeNetwork(ctx, altered, domain.AuditEvent{}); !errors.Is(err, ErrNetworkOperationConflict) {
		t.Fatal(err)
	}
}

func TestNodePortRejectsConflictsAndInvalidPortsWithoutMutation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	before, _ := s.GetNode(ctx, in.NodeID)
	peer := impactPeer(t, s, *before.ServerID, "vless", true)
	for _, port := range []int{0, -1, 65536, peer.ListenPort} {
		in.ListenPort = &port
		if _, err := s.ReviewNodeNetwork(ctx, before.ID, in.Network, nil, &port); err == nil {
			t.Fatal("invalid port accepted", port)
		}
		after, _ := s.GetNode(ctx, before.ID)
		if after.ListenPort != before.ListenPort || after.NetworkRevision != before.NetworkRevision {
			t.Fatal("rejected draft mutated node")
		}
	}
	// A reservation made after preview must be caught in the writer transaction.
	port := 34567
	in.ListenPort = &port
	view, err := s.ReviewNodeNetwork(ctx, before.ID, in.Network, nil, &port)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedImpact = view.Impact.Token
	peer.ListenPort, peer.Port = port, port
	if err = s.UpdateNode(ctx, &peer); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrListenPortConflict) {
		t.Fatal(err)
	}
}
