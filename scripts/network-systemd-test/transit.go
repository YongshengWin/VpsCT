//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"ctlvps/internal/agent"
	"ctlvps/internal/assets"
	"ctlvps/internal/auth"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
)

func startFixtureAgent() {
	_, unit, ok := strings.Cut(string(assets.InstallAgent), "cat > /etc/systemd/system/ctlvps-agent.service <<EOF\n")
	if !ok {
		panic("installer unit missing")
	}
	unit, _, ok = strings.Cut(unit, "\nEOF")
	if !ok {
		panic("installer unit incomplete")
	}
	unit = strings.NewReplacer("$BIN_DIR", "/usr/local/bin", "$STATE_DIR", "/var/lib/ctlvps-agent").Replace(unit)
	if strings.Contains(unit, "$") {
		panic("unhandled installer expansion")
	}
	unit = strings.Replace(unit, "run --state /var/lib/ctlvps-agent", "run --state /var/lib/ctlvps-agent --hold-updates", 1)
	must(os.WriteFile("/etc/systemd/system/ctlvps-agent.service", []byte(unit+"\n"), 0644))
	run("systemctl", "daemon-reload")
	run("systemctl", "start", "ctlvps-agent.service")
}

type transitEnrollment struct{ URL, Token string }

// Runs in the second disposable systemd container, never in a real VPS.
func transitLanding() {
	eventually("transit enrollment instruction", func() bool { _, e := os.Stat("/coord/enroll.json"); return e == nil })
	var in transitEnrollment
	raw, e := os.ReadFile("/coord/enroll.json")
	must(e)
	must(json.Unmarshal(raw, &in))
	copyFile("/coord/ca.crt", "/usr/local/share/ca-certificates/fixture.crt", 0644)
	run("update-ca-certificates")
	copyFile("/fixtures/ctlvps-agent", "/usr/local/bin/ctlvps-agent", 0755)
	copyFile("/fixtures/sing-box", "/opt/ctlvps/bin/sing-box", 0755)
	raw, e = os.ReadFile("/opt/ctlvps/bin/sing-box")
	must(e)
	sum := sha256.Sum256(raw)
	writeJSON("/opt/ctlvps/bin/sing-box.trusted", map[string]string{"Version": corecompat.NetworkBaseline, "SHA256": hex.EncodeToString(sum[:])})
	// Separate origin namespace makes an actual outbound connection; the
	// production proxy guard correctly rejects destinations on its own host.
	run("ip", "netns", "add", "origin")
	run("ip", "link", "add", "origin0", "type", "veth", "peer", "name", "origin1")
	run("ip", "link", "set", "origin1", "netns", "origin")
	run("ip", "addr", "add", "203.0.113.2/24", "dev", "origin0")
	run("ip", "link", "set", "origin0", "up")
	run("ip", "netns", "exec", "origin", "ip", "link", "set", "lo", "up")
	run("ip", "netns", "exec", "origin", "ip", "addr", "add", "203.0.113.10/24", "dev", "origin1")
	for _, address := range []string{"192.0.2.2/32", "198.51.100.2/32"} {
		run("ip", "netns", "exec", "origin", "ip", "addr", "add", address, "dev", "lo")
	}
	run("ip", "netns", "exec", "origin", "ip", "-6", "addr", "add", "2001:db8:ffff::10/128", "dev", "lo", "nodad")
	run("ip", "netns", "exec", "origin", "ip", "link", "set", "origin1", "up")
	stop := process(context.Background(), "ip", "netns", "exec", "origin", "/fixtures/probe", "echo")
	defer stop()
	eventually("landing origin reachable", func() bool {
		c, e := net.DialTimeout("tcp", "203.0.113.10:18080", time.Second)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	run("/usr/local/bin/ctlvps-agent", "enroll", "--server", in.URL, "--token", in.Token)
	state, e := agent.LoadState("/var/lib/ctlvps-agent")
	must(e)
	state.PollIntervalSec = 10
	must(state.Save("/var/lib/ctlvps-agent"))
	startFixtureAgent()
	<-time.After(9 * time.Minute)
}

func testManagedTransit(ctx context.Context, st *store.Store, d *desired.Builder, shares *share.Manager, entry domain.Server, interfaces map[string]string, admin *fixtureAdmin, tls *httptest.Server) {
	landing := domain.Server{Name: "paired landing fixture", PublicHost: "198.18.37.3", CoreMode: domain.CoreModeStable, Enabled: true}
	must(st.CreateServer(ctx, &landing))
	token := auth.RandomToken(24)
	must(st.SetAgentEnrollToken(ctx, landing.ID, auth.HashToken(token), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)))
	must(os.WriteFile("/coord/ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tls.Certificate().Raw}), 0644))
	writeJSON("/coord/enroll-pending.json", transitEnrollment{tls.URL, token})
	must(os.Rename("/coord/enroll-pending.json", "/coord/enroll.json"))
	eventually("second actual agent inventory", func() bool {
		v, e := st.Network(ctx, landing.ID)
		return e == nil && v.Snapshot != nil && v.Snapshot.Status == "ok"
	})
	sh := domain.Share{Name: "paired transit fixture", Targets: []domain.ShareTarget{{ServerID: entry.ID, Protocols: []string{"ss"}}}}
	_, e := shares.Create(ctx, &sh)
	must(e)
	nodes, e := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	must(e)
	if len(nodes) != 1 {
		panic("missing entry")
	}
	n := nodes[0]
	dns := networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}
	protocol := "wireguard"
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "transit-ss2022" {
		protocol = "ss2022"
	}
	in := store.TransitRequest{Protocol: protocol, ID: fmt.Sprintf("%032x", 801), Family: "ipv4", EntryNodeID: n.ID, ExpectedRevision: n.NetworkRevision, LandingServerID: landing.ID, LandingAddress: landing.PublicHost, LandingPort: 25115, Outer: networkconfig.Direct{InterfaceID: interfaces["eth0"], Family: "ipv4", DNS: dns}, DNS: dns}
	var review store.TransitPreview
	admin.request("POST", "/api/v1/transits/preview", in, 200, &review)
	if !review.Ready {
		panic(fmt.Sprintf("transit readiness: %+v", review.Checks))
	}
	in.ExpectedImpact = review.Token
	var op store.ManagedTransit
	admin.request("POST", "/api/v1/transits", in, 202, &op)
	waitStage := func(stage string) {
		eventually("paired stage "+stage, func() bool {
			must(d.ReconcileNetworkOperations(ctx))
			current, e := st.ManagedTransit(ctx, op.ID)
			return e == nil && current.Stage == stage
		})
	}
	waitStage("applied")
	n, e = st.GetNode(ctx, n.ID)
	must(e)
	stop := startClient(ctx, 0, n)
	defer stop()
	eventually("paired transit TCP", func() bool { got, e := probe(0, "tcp", "203.0.113.10"); return e == nil && got == "203.0.113.2" })
	for _, transport := range []string{"udp", "greeting"} {
		got, e := probe(0, transport, "203.0.113.10")
		if e != nil || got != "203.0.113.2" {
			panic(fmt.Sprintf("paired %s: %v", transport, e))
		}
	}
	eventually("separate transit and share ledgers", func() bool {
		sh, e := st.GetShare(ctx, sh.ID)
		if e != nil || sh.UsedUpload == 0 || sh.UsedDownload == 0 {
			return false
		}
		var count int
		e = st.DB().QueryRow(`SELECT count(*) FROM traffic_daily WHERE subject='transit' AND subject_id=? AND up>0 AND down>0`, op.LandingNodeID).Scan(&count)
		return e == nil && count > 0
	})
	// Reconstruct the worker from durable state before retirement.
	d = desired.New(st)
	admin.request("POST", "/api/v1/transits/"+op.ID+"/retire-preview", nil, 200, &review)
	admin.request("DELETE", "/api/v1/transits/"+op.ID, map[string]any{"expected_impact": review.Token}, 202, nil)
	waitStage("retired")
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		panic("retired transit fell back to direct")
	}
	var count int
	must(st.DB().QueryRow(`SELECT count(*) FROM server_listener_reservations WHERE resource_kind='node' AND resource_id=?`, op.LandingNodeID).Scan(&count))
	if count != 0 {
		panic("retired landing port retained")
	}
	fmt.Println("PASS paired managed " + protocol + ": real API -> two real agents/systemd -> TCP/UDP/server-first -> separate ledgers -> durable restart -> ordered retirement -> final meter cleanup -> port release -> no direct fallback")
}
