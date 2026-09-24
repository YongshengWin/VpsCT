package secureupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestAgentNetworkDowngradePreflight(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	binary := filepath.Join(dir, "candidate")
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	// A legacy installation without sticky network state doesn't need the new
	// capability command. Once activated, that contract survives empty cleanup.
	write(state, `{"server_url":"fixture"}`, 0600)
	if err := CheckAgentCompatibility(context.Background(), binary, state); err != nil {
		t.Fatal(err)
	}
	write(state, `{"network_desired_revision":4,"network_egress_version":1,"network_forward_version":1}`, 0600)
	write(binary, "#!/bin/sh\nexit 2\n", 0700)
	if err := CheckAgentCompatibility(context.Background(), binary, state); err == nil {
		t.Fatal("legacy binary accepted for active network state")
	}
	write(binary, "#!/bin/sh\nprintf '%s' '{\"schema\":1,\"network_binding\":1,\"network_egress\":1,\"network_forward\":1,\"network_billing\":1}'\n", 0700)
	if err := CheckAgentCompatibility(context.Background(), binary, state); err != nil {
		t.Fatal(err)
	}
	r, err := readAgentRequirements(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"binding", "egress", "forward"} {
		c := networkconfig.CurrentCapabilities()
		switch kind {
		case "binding":
			c.NetworkBinding = 0
		case "egress":
			c.NetworkEgress = 0
		case "forward":
			c.NetworkForward = 0
		}
		if r.check(c) == nil {
			t.Fatal("lost required", kind)
		}
	}
	write(state, `{"network_ssh_version":1}`, 0600)
	for _, feature := range []string{"listen_binding_version", "forward_dns_version", "forward_private_version", "forward_transport_version"} {
		write(state, `{"`+feature+`":1}`, 0600)
		requirements, err := readAgentRequirements(state)
		if err != nil {
			t.Fatal(err)
		}
		current := networkconfig.CurrentCapabilities()
		if requirements.check(current) != nil {
			t.Fatal("current feature rejected", feature)
		}
		current.ListenBinding, current.ForwardDNS, current.ForwardPrivate, current.ForwardTransport = 0, 0, 0, 0
		if requirements.check(current) == nil {
			t.Fatal("lost required feature", feature)
		}
	}
	write(state, `{"network_ssh_version":1}`, 0600)
	sshRequirements, err := readAgentRequirements(state)
	if err != nil {
		t.Fatal(err)
	}
	oldSSH := networkconfig.CurrentCapabilities()
	oldSSH.NetworkSSH = 0
	if sshRequirements.check(oldSSH) == nil {
		t.Fatal("SSH contract allowed downgrade to SOCKS-only executable")
	}
	write(state, `{"network_billing_pending":{}}`, 0600)
	r, err = readAgentRequirements(state)
	if err != nil {
		t.Fatal(err)
	}
	c := networkconfig.CurrentCapabilities()
	c.NetworkBilling = 0
	if r.check(c) == nil {
		t.Fatal("pending billing cutover lost compatibility guard")
	}
	write(state, `{"network_forward_version":`, 0600)
	if err := CheckAgentCompatibility(context.Background(), binary, state); err == nil {
		t.Fatal("corrupted requirements allowed update")
	}
}
