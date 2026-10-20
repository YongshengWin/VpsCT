package secureupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestUDPForwardStatePreventsAgentDowngrade(t *testing.T) {
	dir := t.TempDir()
	state, binary := filepath.Join(dir, "state.json"), filepath.Join(dir, "candidate")
	if err := os.WriteFile(state, []byte(`{"forward_udp_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{`{"schema":1,"network_binding":1,"network_forward":1}`, `{"schema":1,"network_binding":1,"network_forward":1,"forward_udp":1}`} {
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '"+capability+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		err := CheckAgentCompatibility(context.Background(), binary, state)
		if (err == nil) != strings.Contains(capability, `"forward_udp":1`) {
			t.Fatal("wrong UDP downgrade decision", err)
		}
	}
}

func TestOpenRCInstallationCannotSynchronizeSystemdOnlyAgent(t *testing.T) {
	dir := t.TempDir()
	state, binary := filepath.Join(dir, "state.json"), filepath.Join(dir, "candidate")
	if err := os.WriteFile(state, []byte(`{"openrc_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	// OpenRC alone must trigger preflight even without network or NAT state.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckAgentCompatibility(context.Background(), binary, state); err == nil {
		t.Fatal("OpenRC accepted a legacy executable without capabilities")
	}
	for _, compatible := range []bool{false, true} {
		c := networkconfig.CurrentCapabilities()
		if !compatible {
			c.OpenRC = 0
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '"+string(raw)+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		err = CheckAgentCompatibility(context.Background(), binary, state)
		if (err == nil) != compatible {
			t.Fatalf("OpenRC compatibility=%v result=%v", compatible, err)
		}
	}
	for _, raw := range []string{`{"openrc_version":-1}`, `{"openrc_version":2}`} {
		if err := os.WriteFile(state, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := CheckAgentCompatibility(context.Background(), binary, state); err == nil {
			t.Fatal("invalid persisted service contract ignored")
		}
	}
}

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

func TestNATCapabilityIsBackwardCompatibleAndBlocksControllerDowngrade(t *testing.T) {
	for _, tc := range []struct {
		required, candidate int
		allowed             bool
	}{
		{1, 0, false}, {1, 1, true}, {1, 2, true},
		{2, 0, false}, {2, 1, false}, {2, 2, true},
	} {
		c := networkconfig.CurrentCapabilities()
		c.NAT = tc.candidate
		if err := (agentRequirements{NAT: tc.required}).check(c); (err == nil) != tc.allowed {
			t.Fatalf("required=%d candidate=%d allowed=%v err=%v", tc.required, tc.candidate, tc.allowed, err)
		}
	}
	if networkconfig.CurrentCapabilities().NAT != 2 {
		t.Fatal("current agent does not advertise controller NAT support")
	}
}

func TestAgentNATDowngradePreflight(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	binary := filepath.Join(dir, "candidate")
	if err := os.WriteFile(state, []byte(`{"nat_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '{\"schema\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckAgentCompatibility(context.Background(), binary, state); err == nil {
		t.Fatal("old binary accepted for locally configured NAT")
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '{\"schema\":1,\"nat\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckAgentCompatibility(context.Background(), binary, state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte(`{"nat_version":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckAgentCompatibility(context.Background(), binary, state); err == nil {
		t.Fatal("unsupported persisted NAT contract ignored")
	}
}

func TestOpenRCMieruCannotDowngradeToSingboxOnlyBackend(t *testing.T) {
	r := agentRequirements{OpenRC: 1, OpenRCMieru: 1}
	c := networkconfig.CurrentCapabilities()
	if err := r.check(c); err != nil {
		t.Fatal(err)
	}
	c.OpenRCMieru = 0
	if err := r.check(c); err == nil {
		t.Fatal("accepted loss of OpenRC process accounting")
	}
}

func TestUDPIsolationStateRejectsEarlierReaper(t *testing.T) {
	r := agentRequirements{ForwardUDP: 2}
	c := networkconfig.CurrentCapabilities()
	if err := r.check(c); err != nil {
		t.Fatal(err)
	}
	c.ForwardUDP = 1
	c.ForwardUDPIsolation = 0
	if r.check(c) == nil {
		t.Fatal("isolation state accepted old shared-core-stop reaper")
	}
	r.ForwardUDP = 1
	c.ForwardUDP = 2
	if err := r.check(c); err != nil {
		t.Fatal("old UDP state cannot upgrade", err)
	}
}

func TestUDPIsolationCandidateRemainsRecognizableToLegacyUpdater(t *testing.T) {
	c := networkconfig.CurrentCapabilities()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		ForwardUDP int `json:"forward_udp"`
	}
	if err = json.Unmarshal(raw, &legacy); err != nil || legacy.ForwardUDP != 1 {
		t.Fatal("installed v1 updater would reject candidate", err)
	}
	if err = (agentRequirements{ForwardUDP: 2}).check(c); err != nil {
		t.Fatal("isolated candidate rejected", err)
	}
	c.ForwardUDPIsolation = 0
	if (agentRequirements{ForwardUDP: 2}).check(c) == nil {
		t.Fatal("legacy candidate admitted after isolation activation")
	}
}
