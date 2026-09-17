package agent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/maintenance"
)

func (a *Agent) maintenanceSupported() bool {
	u, err := url.Parse(a.State.ServerURL)
	return !a.HoldUpdates && maintenance.Supported() && a.StateDir == "/var/lib/ctlvps-agent" && executablePath() == "/usr/local/bin/ctlvps-agent" && err == nil && u.Scheme == "https" && u.User == nil
}

func (a *Agent) maintain(ctx context.Context, c agentproto.MaintenanceCommand) error {
	if !a.maintenanceSupported() || c.Request.Role != "agent" {
		return fmt.Errorf("agent installation does not support web maintenance")
	}
	if err := c.Request.Validate(); err != nil {
		return err
	}
	path := "/api/maintenance/v1/jobs/" + c.Request.ID
	// A fresh claim prevents delayed heartbeats from executing an expired job.
	client := NewClient(a.State.ServerURL, c.ReportToken, a.Version)
	if err := client.do(ctx, "POST", path+"/claim", map[string]bool{}, nil, false); err != nil {
		return err
	}
	spec := maintenance.Spec{Request: c.Request, SHA256: c.SHA256, CallbackURL: strings.TrimRight(a.State.ServerURL, "/") + path + "/report", CallbackToken: c.ReportToken}
	if c.Request.Action == "update" {
		spec.DownloadURL = strings.TrimRight(a.State.ServerURL, "/") + "/dl/agent/linux-" + runtime.GOARCH
	}
	m := maintenance.NewManager()
	j, err := m.Start(spec)
	if errors.Is(err, maintenance.ErrBusy) {
		// A co-located controller may still be finishing its own upgrade. Keep
		// this claimed job pending and retry the same ID on the next heartbeat.
		return nil
	}
	if err != nil {
		if j.ID == "" {
			j = maintenance.Job{Request: c.Request, Status: "failed", Stage: "dispatch", Message: "无法启动维护任务，请检查本机是否已有任务运行及 systemd 日志", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		}
		_ = client.do(ctx, "POST", path+"/report", j, nil, false)
		return err
	}
	// Repeated heartbeat delivery is idempotent. If a worker finished while the
	// panel was unavailable, the next heartbeat delivers its durable result.
	if !j.Active() {
		return client.do(ctx, "POST", path+"/report", j, nil, false)
	}
	return nil
}

func (a *Agent) maintenanceAction() string {
	if !a.maintenanceSupported() {
		return ""
	}
	m := maintenance.NewManager()
	m.Recover()
	action, err := m.ActiveAction("agent")
	if err != nil {
		return "pending"
	}
	return action
}
