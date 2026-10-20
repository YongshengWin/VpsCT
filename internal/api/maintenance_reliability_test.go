package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/store"
)

func agentUpdateFixture(t *testing.T) (*client, int64, *time.Time, string) {
	t.Helper()
	c := newTestAPI(t)
	c.api.Config.Version = "v0.1.5 (abcdef1)"
	c.api.Config.AgentBinDir = t.TempDir()
	content := []byte("fixture release A")
	if err := os.WriteFile(filepath.Join(c.api.Config.AgentBinDir, "ctlvps-agent-linux-amd64"), content, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	c.api.Store.Now = func() time.Time { return now }
	srv := &domain.Server{Name: "update-fixture", Enabled: true}
	if err := c.api.Store.CreateServer(context.Background(), srv); err != nil {
		t.Fatal(err)
	}
	ag, err := c.api.Store.GetAgentByServer(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := json.Marshal(agentproto.Metrics{Arch: "amd64"})
	diagnostics, _ := json.Marshal(agentproto.Diagnostics{BinarySHA256: strings.Repeat("0", 64), SecurityVersion: 1, SecurityPolicy: true, Maintenance: 1})
	if err := c.api.Store.Heartbeat(context.Background(), ag.ID, "v0.1.4", "", "", metrics, diagnostics); err != nil {
		t.Fatal(err)
	}
	return c, srv.ID, &now, fmt.Sprintf("%x", sha256.Sum256(content))
}

func fixtureUpdateJob(t *testing.T, c *client, sid int64, target, status, stage string, when time.Time) store.MaintenanceJob {
	t.Helper()
	j := store.MaintenanceJob{Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "update", Version: "v0.1.5"}, PreviousSHA256: strings.Repeat("0", 64), Status: status, Stage: stage, CreatedAt: when, UpdatedAt: when}, ServerID: sid, AgentSHA: target, ReportToken: auth.RandomToken(32)}
	if err := c.api.Store.CreateMaintenance(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestAutomaticAgentUpdateRetriesOnlyBoundedDownloadFailures(t *testing.T) {
	c, sid, now, sha := agentUpdateFixture(t)
	ctx := context.Background()
	for attempt := 1; attempt <= 3; attempt++ {
		c.api.queueAutomaticAgentUpdate(ctx, sid, sha)
		j, err := c.api.Store.LatestAgentUpdate(ctx, sid, sha)
		if err != nil || j.Status != "queued" || j.Version != "v0.1.5" || j.PreviousSHA256 == "" {
			t.Fatalf("attempt %d was not queued with a pinned release: %+v %v", attempt, j, err)
		}
		j.Status, j.Stage = "failed", "download_retryable"
		if err := c.api.Store.SaveMaintenance(ctx, j, "queued"); err != nil {
			t.Fatal(err)
		}
		c.api.queueAutomaticAgentUpdate(ctx, sid, sha)
		latest, _ := c.api.Store.LatestAgentUpdate(ctx, sid, sha)
		if latest.ID != j.ID {
			t.Fatal("download retry ignored durable backoff")
		}
		*now = now.Add(6 * time.Minute)
	}
	c.api.queueAutomaticAgentUpdate(ctx, sid, sha)
	jobs, _ := c.api.Store.ListMaintenance(ctx, sid)
	if len(jobs) != 3 || !c.api.agentMaintenanceNeedsRetry(ctx, sid, sha) {
		t.Fatal("download retries exceeded three attempts or failed to request manual action", len(jobs))
	}
	for _, state := range []string{"failed", "rolled_back", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			other, id, at, target := agentUpdateFixture(t)
			j := fixtureUpdateJob(t, other, id, target, state, state, at.Add(-time.Hour))
			other.api.queueAutomaticAgentUpdate(ctx, id, target)
			got, _ := other.api.Store.LatestAgentUpdate(ctx, id, target)
			if got.ID != j.ID || !other.api.agentMaintenanceNeedsRetry(ctx, id, target) {
				t.Fatal("unsafe trust, compatibility or rollback failure retried automatically")
			}
		})
	}
}

func TestExpiredAgentUpdateCanQueueAgain(t *testing.T) {
	c, sid, now, sha := agentUpdateFixture(t)
	ctx := context.Background()
	old := fixtureUpdateJob(t, c, sid, sha, "queued", "queued", now.Add(-16*time.Minute))
	c.api.queueAutomaticAgentUpdate(ctx, sid, sha)
	latest, err := c.api.Store.LatestAgentUpdate(ctx, sid, sha)
	previous, _ := c.api.Store.GetMaintenance(ctx, old.ID)
	attempts, _ := c.api.Store.AgentUpdateAttempts(ctx, sid, sha)
	if err != nil || latest.ID == old.ID || latest.Status != "queued" || previous.Status != "expired" || attempts != 1 {
		t.Fatalf("unclaimed expiration permanently blocked synchronization: %+v %+v %d %v", latest, previous, attempts, err)
	}
}

func TestAgentUpdateCancelsUnclaimedSupersededTarget(t *testing.T) {
	for _, mode := range []string{"delayed claim", "new heartbeat", "pending command"} {
		t.Run(mode, func(t *testing.T) {
			c, sid, now, sha := agentUpdateFixture(t)
			ctx := context.Background()
			old := fixtureUpdateJob(t, c, sid, sha, "queued", "queued", *now)
			newBytes := []byte("fixture newer release B with different bytes")
			if err := os.WriteFile(filepath.Join(c.api.Config.AgentBinDir, "ctlvps-agent-linux-amd64"), newBytes, 0600); err != nil {
				t.Fatal(err)
			}
			newSHA := fmt.Sprintf("%x", sha256.Sum256(newBytes))
			c.api.Config.Version = "v0.1.6"
			switch mode {
			case "delayed claim":
				// A delayed heartbeat command cannot start the old target.
				maintenanceHTTP(t, c, "/api/maintenance/v1/jobs/"+old.ID+"/claim", map[string]bool{}, "", old.ReportToken, 409)
			case "new heartbeat":
				c.api.queueAutomaticAgentUpdate(ctx, sid, newSHA)
			case "pending command":
				c.api.nextAgentMaintenance(ctx, sid)
			}
			saved, _ := c.api.Store.GetMaintenance(ctx, old.ID)
			if saved.Status != "cancelled" || saved.ReportToken != "" {
				t.Fatal("superseded task remained executable")
			}
			c.api.queueAutomaticAgentUpdate(ctx, sid, newSHA)
			command := c.api.nextAgentMaintenance(ctx, sid)
			if command == nil || command.Request.ID == old.ID || command.SHA256 != newSHA || command.Request.Version != "v0.1.6" {
				t.Fatal("latest immutable target was not queued", command)
			}
		})
	}
}

func TestAgentUpdateDoesNotReplaceAClaimedTask(t *testing.T) {
	c, sid, now, sha := agentUpdateFixture(t)
	ctx := context.Background()
	old := fixtureUpdateJob(t, c, sid, sha, "running", "restart", *now)
	c.api.queueAutomaticAgentUpdate(ctx, sid, strings.Repeat("a", 64))
	jobs, _ := c.api.Store.ListMaintenance(ctx, sid)
	if len(jobs) != 1 || jobs[0].ID != old.ID || jobs[0].Status != "running" {
		t.Fatal("new target overlapped an active upgrade")
	}
}

func TestAgentUpdateFailureHistoryIsNotLimitedToPanelRows(t *testing.T) {
	c, sid, now, sha := agentUpdateFixture(t)
	ctx := context.Background()
	old := fixtureUpdateJob(t, c, sid, sha, "failed", "failed", now.Add(-time.Hour))
	for i := 0; i < 25; i++ {
		fixtureUpdateJob(t, c, sid, strings.Repeat("a", 64), "failed", "failed", now.Add(time.Duration(i)*time.Second))
	}
	c.api.queueAutomaticAgentUpdate(ctx, sid, sha)
	latest, _ := c.api.Store.LatestAgentUpdate(ctx, sid, sha)
	if latest.ID != old.ID || !c.api.agentMaintenanceNeedsRetry(ctx, sid, sha) {
		t.Fatal("older safety failure fell outside the display window and retried")
	}
}

func TestAgentHeartbeatReconcilesLostOutcomeWithoutPrematureSuccess(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, status, previous string
		age                    time.Duration
		want                   string
	}{
		{"running worker may still roll back", "running", strings.Repeat("0", 64), time.Minute, "running"},
		{"lost outcome after worker lifetime", "running", strings.Repeat("0", 64), 61 * time.Minute, "succeeded"},
		{"interrupted outcome recovers", "interrupted", strings.Repeat("0", 64), 61 * time.Minute, "succeeded"},
		{"rollback remains visible", "rolled_back", strings.Repeat("0", 64), 61 * time.Minute, "rolled_back"},
		{"legacy task has no initial hash", "interrupted", "", 61 * time.Minute, "interrupted"},
		{"same hash reinstall needs receipt", "interrupted", "same", 61 * time.Minute, "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, sid, now, sha := agentUpdateFixture(t)
			j := fixtureUpdateJob(t, c, sid, sha, tc.status, tc.status, now.Add(-tc.age))
			previous := tc.previous
			if previous == "same" {
				previous = sha
			}
			j.PreviousSHA256 = previous
			if err := c.api.Store.SaveMaintenance(ctx, j, tc.status); err != nil {
				t.Fatal(err)
			}
			// SaveMaintenance deliberately updates UpdatedAt, so advance the
			// fixture clock to exercise controller expiry and a fresh heartbeat.
			if tc.age > time.Hour {
				*now = now.Add(61 * time.Minute)
			}
			c.api.reconcileAgentMaintenance(ctx, sid, sha)
			got, _ := c.api.Store.GetMaintenance(ctx, j.ID)
			if got.Status != tc.want {
				t.Fatalf("unsafe outcome reconciliation: %+v", got.Job)
			}
			if tc.want == "succeeded" && (got.Stage != "heartbeat_confirmed" || got.ReportToken != "") {
				t.Fatal("recovery left callback credentials or failed to distinguish heartbeat confirmation")
			}
		})
	}
}

func TestAgentHeartbeatEndpointReconcilesOnlyChangedTarget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, previous, reported, want string
	}{
		{"lost terminal report", "old", "target", "succeeded"},
		{"same file reinstall", "target", "target", "interrupted"},
		{"different running file", "old", "different", "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, sid, now, target := agentUpdateFixture(t)
			ag, err := c.api.Store.GetAgentByServer(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			enrollHash := auth.HashToken("maintenance-fixture-enrollment")
			if err := c.api.Store.SetAgentEnrollToken(ctx, sid, enrollHash, now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			c.agent = "maintenance-fixture-agent"
			if err := c.api.Store.CompleteEnrollment(ctx, ag.ID, enrollHash, auth.HashToken(c.agent), "v0.1.4"); err != nil {
				t.Fatal(err)
			}
			j := fixtureUpdateJob(t, c, sid, target, "running", "restart", *now)
			if tc.previous == "target" {
				j.PreviousSHA256 = target
				if err := c.api.Store.SaveMaintenance(ctx, j, "running"); err != nil {
					t.Fatal(err)
				}
			}
			reported := target
			if tc.reported == "different" {
				reported = strings.Repeat("b", 64)
			}
			*now = now.Add(61 * time.Minute)
			hb := agentproto.Heartbeat{Version: "v0.1.5", TS: *now, Epoch: "maintenance-fixture", Metrics: agentproto.Metrics{Arch: "amd64"}, Diagnostics: agentproto.Diagnostics{BinarySHA256: reported, SecurityVersion: 1, SecurityPolicy: true, Maintenance: 1}}
			// Exercise the authenticated endpoint, not just the reconciliation
			// helper: the new heartbeat must be persisted and reconciled together.
			c.do("POST", "/api/agent/v1/heartbeat", hb, http.StatusOK)
			c.do("POST", "/api/agent/v1/heartbeat", hb, http.StatusOK)
			got, err := c.api.Store.GetMaintenance(ctx, j.ID)
			jobs, listErr := c.api.Store.ListMaintenance(ctx, sid)
			if err != nil || listErr != nil || got.Status != tc.want || len(jobs) != 1 {
				t.Fatalf("fresh heartbeat lost or invented an outcome: %+v jobs=%d errors=%v/%v", got.Job, len(jobs), err, listErr)
			}
			if tc.want == "succeeded" {
				if got.Stage != "heartbeat_confirmed" || got.ReportToken != "" {
					t.Fatal("heartbeat confirmation retained execution credentials or lost its source")
				}
				// A worker's delayed callback cannot overwrite the reconciled
				// terminal result, even while its scoped report hash is retained.
				report := j.Job
				report.Status, report.Stage = "running", "restart"
				maintenanceHTTP(t, c, "/api/maintenance/v1/jobs/"+j.ID+"/report", report, "", j.ReportToken, http.StatusOK)
				saved, _ := c.api.Store.GetMaintenance(ctx, j.ID)
				if saved.Status != "succeeded" || saved.Stage != "heartbeat_confirmed" {
					t.Fatal("late worker callback resurrected a completed upgrade")
				}
			}
		})
	}
}

func TestAgentDownloadRejectsChangedTargetAndPreservesLegacyRoute(t *testing.T) {
	c, _, _, sha := agentUpdateFixture(t)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"?sha256=" + sha, 200},
		{"?sha256=" + strings.ToUpper(sha), 200},
		{"?sha256=" + strings.Repeat("a", 64), 409},
		{"?sha256=invalid", 400},
		{"?sha256=" + strings.Repeat("z", 64), 400},
		{"", 200},
	} {
		response, err := c.srv.Client().Get(c.srv.URL + "/dl/agent/linux-amd64" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != tc.status {
			t.Fatalf("download response %d, expected %d: %v", response.StatusCode, tc.status, err)
		}
		if tc.status == http.StatusOK && string(body) != "fixture release A" {
			t.Fatal("different executable bytes served")
		}
	}
}
