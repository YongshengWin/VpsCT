package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/nft"
	"ctlvps/internal/secureupdate"
)

type restartFixtureTransport func(*http.Request) (*http.Response, error)

func (f restartFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestColdStartRechecksAppliedRevisionAndReportsFailure(t *testing.T) {
	// No host policy, service or firewall is changed. Root Linux needs the
	// configuration lock's real directory, so reserve it for a disposable host.
	if runtime.GOOS == "linux" && os.Geteuid() == 0 && os.Getenv("CTLVPS_SYSTEMD_TEST") != "1" {
		t.Skip("root Linux configuration lock requires a disposable container")
	}
	if p, err := secureupdate.LoadPolicy(); err != nil || p.PauseConfig || secureupdate.Allow("agent.configure") != nil {
		t.Skip("local policy does not permit the fixture configuration")
	}
	dir := t.TempDir()
	ds := &agentproto.DesiredState{ServerID: 1, Revision: 4, Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: "anytls", ListenPort: 20443, Params: map[string]any{"password": "fixture-only"}}}}
	ds.Hash = agentproto.ContentHash(ds)
	old := &State{ServerURL: "https://fixture.example.test", AgentToken: "fixture", ServerID: 1,
		MeteringV1: true, AppliedRevision: ds.Revision, AppliedHash: ds.Hash,
		NodeApplicationRevision: ds.Revision, NodeApplicationHash: ds.Hash, NodeApplicationSequence: 8,
		NodeApplications: []agentproto.NodeApplication{{NodeID: 1, Core: "singbox", Status: "applied"}}}
	if err := old.Save(dir); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	var reports []agentproto.ApplyReport
	fetches := 0
	client := NewClient(old.ServerURL, "fixture", "fixture-upgraded-agent")
	client.HTTP = &http.Client{Transport: restartFixtureTransport(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch req.URL.Path {
		case agentproto.PathDesired:
			fetches++
			body, _ = json.Marshal(ds)
		case agentproto.PathApplyReport:
			var report agentproto.ApplyReport
			if err := json.NewDecoder(req.Body).Decode(&report); err != nil {
				t.Fatal(err)
			}
			reports = append(reports, report)
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	// The missing fixture command fails before privileged core application. A
	// restarted agent must discover that failure even though durable revision
	// and hash still say the old process applied this exact desired state.
	a := &Agent{StateDir: dir, State: state, Client: client,
		NFT: &nft.Manager{Bin: filepath.Join(dir, "unavailable-nft")}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for attempt := 1; attempt <= 2; attempt++ {
		a.converge(context.Background(), false)
		if fetches != attempt || len(reports) != attempt {
			t.Fatal("same-revision cold start or failure retry skipped configuration verification")
		}
		report := reports[attempt-1]
		if report.Status != "failed" || !strings.Contains(report.Error, "nftables is required") || report.Revision != ds.Revision || report.Hash != ds.Hash {
			t.Fatalf("old applied state hid current failure: %+v", report)
		}
		persisted, err := LoadState(dir)
		if err != nil || persisted.ApplyError == "" || a.desired != nil {
			t.Fatalf("failed cold-start verification was not durable: %v", err)
		}
		if persisted.AppliedRevision != old.AppliedRevision || persisted.AppliedHash != old.AppliedHash || persisted.NodeApplicationSequence != old.NodeApplicationSequence {
			t.Fatal("failure invented an applied revision or successful application receipt")
		}
	}
}
