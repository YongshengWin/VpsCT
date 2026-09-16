package api

// This opt-in fixture serves the real frontend/API with an inert maintenance
// backend. It cannot invoke systemd or touch an installed server.
import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/maintenance"
	"ctlvps/web"
)

type previewMaintenance struct {
	mu   sync.Mutex
	jobs []maintenance.Job
}

func (p *previewMaintenance) Call(_ context.Context, method, path string, in, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if path == "/latest" {
		*(out.(*maintenance.Release)) = maintenance.Release{Version: "v0.2.0", URL: "https://github.com/YongshengWin/VpsCT/releases"}
		return nil
	}
	if method == "GET" {
		for i := range p.jobs {
			if time.Since(p.jobs[i].CreatedAt) > 5*time.Second {
				p.jobs[i].Status = "succeeded"
				p.jobs[i].Message = "演示任务完成（未执行系统操作）"
			}
		}
		*(out.(*maintenance.Info)) = maintenance.Info{Available: true, Version: "v0.1.0", Jobs: append([]maintenance.Job{}, p.jobs...)}
		return nil
	}
	r := in.(maintenance.Request)
	j := maintenance.Job{Request: r, Status: "running", Stage: "upgrade", Message: "演示任务执行中（未执行系统操作）", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	p.jobs = append([]maintenance.Job{j}, p.jobs...)
	*(out.(*maintenance.Job)) = j
	return nil
}

func TestMaintenanceBrowserPreview(t *testing.T) {
	if os.Getenv("CTLVPS_TEST_PREVIEW") != "1" {
		t.Skip("opt-in inert browser fixture")
	}
	c := newTestAPI(t)
	c.api.Static = web.Handler("")
	c.api.Deps.Maintenance = &previewMaintenance{}
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "demo-admin", "password": "demo-password-test"}, 200)
	c.do("POST", "/api/v1/servers", map[string]any{"name": "演示服务器", "region": "TEST"}, 201)
	tok := c.do("POST", "/api/v1/servers/1/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: tok["token"].(string), Version: "v0.1.0", Arch: "amd64"}, 200)
	c.agent = en["agent_token"].(string)
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{Version: "v0.1.0", Epoch: "preview", TS: time.Now(), Diagnostics: agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, Maintenance: 1}}, 200)
	t.Log("Inert browser preview:", c.srv.URL)
	time.Sleep(10 * time.Minute)
}
