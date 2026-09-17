package api

import (
	"bytes"
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
)

type fakeMaintenance struct{ calls int }

func TestAgentMaintenanceReportsBinaryUpdateStatus(t *testing.T) {
	content := []byte("test agent binary")
	sha := fmt.Sprintf("%x", sha256.Sum256(content))
	for _, tc := range []struct {
		name, current string
		missingBinary bool
		legacy        bool
		outdated      bool
	}{
		{name: "same binary despite version display suffix", current: strings.ToUpper(sha)},
		{name: "same version with different binary", current: strings.Repeat("0", 64), outdated: true},
		{name: "agent has not reported binary"},
		{name: "legacy agent cannot update", current: strings.Repeat("0", 64), legacy: true},
		{name: "controller binary unavailable", current: sha, missingBinary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestAPI(t)
			c.api.Config.Version = "v0.1.0 (abcdef1)"
			c.api.Config.AgentBinDir = t.TempDir()
			if !tc.missingBinary {
				if err := os.WriteFile(filepath.Join(c.api.Config.AgentBinDir, "ctlvps-agent-linux-amd64"), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
			srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "test-vps"}, 201)
			path := fmt.Sprintf("/api/v1/servers/%.0f", srv["id"])
			et := c.do("POST", path+"/enroll-token", nil, 200)
			en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "v0.1.0", Arch: "amd64"}, 200)
			c.agent = en["agent_token"].(string)
			reply := c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{Version: "v0.1.0", TS: time.Now(), Epoch: "test", Metrics: agentproto.Metrics{Arch: "amd64"}, Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: !tc.legacy, Maintenance: 1, BinarySHA256: tc.current}}, 200)
			if tc.legacy && (reply["agent_update"] != nil || reply["maintenance"] != nil) {
				t.Fatal("legacy agent received executable task")
			}
			status := c.do("GET", path+"/maintenance", nil, 200)
			update := status["agent_update"].(map[string]any)
			if status["available"] != !tc.legacy || update["outdated"] != tc.outdated {
				t.Fatalf("unexpected update status: %v", status)
			}
			if tc.current != "" && update["current_sha"] != tc.current {
				t.Fatalf("current binary not returned: %v", update)
			}
			if !tc.missingBinary && update["latest_sha"] != sha {
				t.Fatalf("target binary not returned: %v", update)
			}
		})
	}
}

func (f *fakeMaintenance) Call(_ context.Context, method, path string, in, out any) error {
	if method == "POST" {
		f.calls++
		r := in.(maintenance.Request)
		*(out.(*maintenance.Job)) = maintenance.Job{Request: r, Status: "queued"}
		return nil
	}
	*(out.(*maintenance.Info)) = maintenance.Info{Available: true, Version: "v0.1.0", Jobs: []maintenance.Job{}}
	return nil
}

func maintenanceHTTP(t *testing.T, c *client, path string, body any, origin, token string, want int) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
		req.Header.Set("X-CSRF-Token", c.api.csrfToken(c.cookie.Value))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d want %d: %s", path, resp.StatusCode, want, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestControllerMaintenanceAuthorization(t *testing.T) {
	c := newTestAPI(t)
	fake := &fakeMaintenance{}
	c.api.Deps.Maintenance = fake
	in := maintenanceInput{Request: maintenance.Request{ID: maintenance.NewID(), Role: "controller", Action: "uninstall"}, Password: "password123", Confirm: "VpsCT"}
	p := "/api/v1/system/maintenance"
	maintenanceHTTP(t, c, p, in, c.srv.URL, "", 401)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	maintenanceHTTP(t, c, p, in, "https://other.example.test", "", 403)
	maintenanceHTTP(t, c, p, in, "", "", 403)
	bad := in
	bad.Password = "wrong"
	maintenanceHTTP(t, c, p, bad, c.srv.URL, "", 403)
	bad = in
	bad.Confirm = "wrong"
	maintenanceHTTP(t, c, p, bad, c.srv.URL, "", 400)
	maintenanceHTTP(t, c, p, in, c.srv.URL, "", 202)
	if fake.calls != 1 {
		t.Fatal("unauthorized request reached executor")
	}
	// A normal account cannot even query maintenance status.
	u, err := c.api.Store.GetUser(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	u.Role = domain.RoleUser
	if err := c.api.Store.UpdateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	c.do("GET", p, nil, 401)
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	c.do("GET", p, nil, 403)
	maintenanceHTTP(t, c, p, in, c.srv.URL, "", 403)
}

func TestMaintenanceSecondFactorCannotReplay(t *testing.T) {
	c := newTestAPI(t)
	fake := &fakeMaintenance{}
	c.api.Deps.Maintenance = fake
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	u, err := c.api.Store.GetUser(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	u.TOTPEnabled, u.TOTPSecret = true, auth.NewTOTPSecret()
	plain, hashes := auth.NewRecoveryCodes(2)
	u.RecoveryCodes = hashes
	if err := c.api.Store.UpdateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	login := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": login["challenge"], "code": plain[1]}, 200)
	in := maintenanceInput{Request: maintenance.Request{ID: maintenance.NewID(), Role: "controller", Action: "update", Version: "v0.2.0"}, Password: "password123"}
	path := "/api/v1/system/maintenance"
	maintenanceHTTP(t, c, path, in, c.srv.URL, "", 403)
	in.Code, _ = auth.TOTPCode(u.TOTPSecret, auth.TOTPStep(c.api.Store.Now()))
	maintenanceHTTP(t, c, path, in, c.srv.URL, "", 202)
	in.ID = maintenance.NewID()
	maintenanceHTTP(t, c, path, in, c.srv.URL, "", 403)
	in.Code = plain[0]
	maintenanceHTTP(t, c, path, in, c.srv.URL, "", 202)
	in.ID = maintenance.NewID()
	maintenanceHTTP(t, c, path, in, c.srv.URL, "", 403)
	if fake.calls != 2 {
		t.Fatal("replayed factor reached maintenance executor")
	}
}

func TestAgentMaintenanceClaimExpiryAndReports(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "test-vps"}, 201)
	sid := int64(srv["id"].(float64))
	et := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/enroll-token", sid), nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "test", Arch: "amd64"}, 200)
	c.agent = en["agent_token"].(string)
	hb := agentproto.Heartbeat{Version: "test", TS: time.Now(), Epoch: "test", Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, Maintenance: 1}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	p := fmt.Sprintf("/api/v1/servers/%d/maintenance", sid)
	in := maintenanceInput{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "uninstall"}, Password: "password123", Confirm: "test-vps"}
	maintenanceHTTP(t, c, p, in, c.srv.URL, "", 202)
	maintenanceHTTP(t, c, p, in, c.srv.URL, "", 200) // retry same request
	other := in
	other.ID = maintenance.NewID()
	maintenanceHTTP(t, c, p, other, c.srv.URL, "", 409)
	c.do("DELETE", fmt.Sprintf("/api/v1/servers/%d", sid), nil, 409)
	j, err := c.api.Store.GetMaintenance(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(j)
	if strings.Contains(string(public), j.ReportToken) {
		t.Fatal("scoped credential leaked")
	}
	jobPath := "/api/maintenance/v1/jobs/" + j.ID
	maintenanceHTTP(t, c, jobPath+"/claim", map[string]bool{}, "", "wrong", 401)
	maintenanceHTTP(t, c, jobPath+"/claim", map[string]bool{}, "", j.ReportToken, 200)
	report := j.Job
	report.Status = "succeeded"
	report.Stage = "succeeded"
	report.Message = "done"
	bad := report
	bad.Purge = true
	maintenanceHTTP(t, c, jobPath+"/report", bad, "", j.ReportToken, 400)
	maintenanceHTTP(t, c, jobPath+"/report", report, "", j.ReportToken, 200)
	maintenanceHTTP(t, c, jobPath+"/report", report, "", j.ReportToken, 200) // duplicate final report
	report.Status = "running"
	maintenanceHTTP(t, c, jobPath+"/report", report, "", j.ReportToken, 200)
	got, _ := c.api.Store.GetMaintenance(context.Background(), j.ID)
	if got.Status != "succeeded" || got.ReportToken != "" {
		t.Fatal("terminal job resurrected or credential retained")
	}
	// Expired queues cannot be claimed after a disconnected machine returns.
	j.ID = maintenance.NewID()
	j.Status = "queued"
	j.CreatedAt = time.Now().Add(-16 * time.Minute)
	j.UpdatedAt = j.CreatedAt
	j.ReportToken = auth.RandomToken(32)
	if err := c.api.Store.CreateMaintenance(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	maintenanceHTTP(t, c, "/api/maintenance/v1/jobs/"+j.ID+"/claim", map[string]bool{}, "", j.ReportToken, 409)
	got, _ = c.api.Store.GetMaintenance(context.Background(), j.ID)
	if got.Status != "expired" {
		t.Fatal("stale queue executed")
	}
}

func TestUninstallThenDeleteServer(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			c := newTestAPI(t)
			c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
			srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "delete-test"}, 201)
			sid := int64(srv["id"].(float64))
			path := fmt.Sprintf("/api/v1/servers/%d", sid)
			in := maintenanceInput{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "uninstall"}, DeleteServer: true, Password: "password123", Confirm: "delete-test"}
			maintenanceHTTP(t, c, path+"/maintenance", in, c.srv.URL, "", 409) // offline retains record
			c.do("GET", path, nil, 200)
			et := c.do("POST", path+"/enroll-token", nil, 200)
			en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "test", Arch: "amd64"}, 200)
			c.agent = en["agent_token"].(string)
			c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{Version: "test", TS: time.Now(), Epoch: "test", Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, Maintenance: 1}}, 200)
			bad := in
			bad.Password = "wrong-password"
			maintenanceHTTP(t, c, path+"/maintenance", bad, c.srv.URL, "", 403)
			bad = in
			bad.Action = "update"
			maintenanceHTTP(t, c, path+"/maintenance", bad, c.srv.URL, "", 400)
			maintenanceHTTP(t, c, path+"/maintenance", in, c.srv.URL, "", 202)
			bad = in
			bad.DeleteServer = false
			maintenanceHTTP(t, c, path+"/maintenance", bad, c.srv.URL, "", 409)
			c.do("GET", path, nil, 200)
			c.do("DELETE", path, nil, 409)
			j, err := c.api.Store.GetMaintenance(context.Background(), in.ID)
			if err != nil || !j.DeleteServer {
				t.Fatalf("deletion intent not persisted: %+v %v", j, err)
			}
			report := j.Job
			report.Status = status
			jobPath := "/api/maintenance/v1/jobs/" + j.ID
			maintenanceHTTP(t, c, jobPath+"/claim", map[string]bool{}, "", j.ReportToken, 200)
			maintenanceHTTP(t, c, jobPath+"/report", report, "", "wrong", 401)
			maintenanceHTTP(t, c, jobPath+"/report", report, "", j.ReportToken, 200)
			maintenanceHTTP(t, c, jobPath+"/report", report, "", j.ReportToken, 200)
			want := 200
			if status == "succeeded" {
				want = 404
			}
			c.do("GET", path, nil, want)
			saved, err := c.api.Store.GetMaintenance(context.Background(), j.ID)
			if err != nil || saved.Status != status {
				t.Fatalf("receipt lost: %+v %v", saved, err)
			}
		})
	}
}
