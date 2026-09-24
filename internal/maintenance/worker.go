package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"ctlvps/internal/diskbudget"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	lifecycle "ctlvps"
	"ctlvps/internal/agentnet"
	"ctlvps/internal/secureupdate"
	"golang.org/x/sys/unix"
	"runtime"
)

func (m *Manager) Run(id string) error {
	j, err := m.Get(id)
	if err != nil || !j.Active() {
		return err
	}
	var s Spec
	if err := readJSON(m.path(id, "request.json"), &s); err != nil {
		return err
	}
	if err := s.Validate(); err != nil || s.Request != j.Request {
		return errors.New("维护任务文件无效")
	}
	if m.Dir == Directory {
		if err := secureupdate.Allow(s.Role + "." + s.Action); err != nil {
			return err
		}
		if s.Purge {
			if err := secureupdate.Allow(s.Role + ".purge"); err != nil {
				return err
			}
		}
	}
	log, err := openLog(m.path(id, "worker.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	// Allow the initiating HTTP response to reach the browser before stopping it.
	time.Sleep(2 * time.Second)
	stage := func(status, step, message string) {
		j.Status, j.Stage, j.Message, j.UpdatedAt = status, step, message, time.Now().UTC()
		_ = writeJSON(m.path(id, "status.json"), j)
		_, _ = fmt.Fprintf(log, "%s %s: %s\n", j.UpdatedAt.Format(time.RFC3339), step, message)
		_ = report(s, j)
	}
	releaseConfiguration := func() {}
	if s.Role == "agent" {
		release, err := m.tryConfigurationLock()
		if err != nil {
			stage("failed", "target_lock", "无法取得 agent 配置锁，未执行维护")
			return err
		}
		releaseConfiguration = release
		defer releaseConfiguration()
	}
	stage("running", "preflight", "正在检查安装路径和服务状态")
	uninstaller := m.path(id, "uninstall.sh")
	err = os.WriteFile(uninstaller, lifecycle.Uninstall, 0700)
	args := []string{uninstaller, "--" + s.Role, "--yes"}
	if s.Purge {
		args = append(args, "--purge")
	}
	if s.RemoveCaddy {
		args = append(args, "--remove-caddy")
	}
	if err == nil {
		err = command(ctx, log, "bash", append(args, "--dry-run")...)
	}
	status, message := "failed", "预检查失败，尚未执行维护；请查看本机维护日志"
	if err == nil && s.Action == "uninstall" {
		stage("running", "uninstall", "正在停止所选端并移除程序；网页可能暂时断开")
		err = command(ctx, log, "bash", args...)
		if err == nil {
			status, message = "succeeded", "卸载完成"
		} else {
			message = "卸载未完成，请检查本机维护日志；未确认完成前不要删除面板记录"
		}
	} else if err == nil && s.Role == "controller" {
		status, message, err = m.updateController(ctx, s, log, stage)
	} else if err == nil {
		status, message, err = m.updateAgent(ctx, s, log, stage)
	}
	if err != nil {
		_, _ = fmt.Fprintln(log, err)
	}
	releaseConfiguration()
	stage(status, status, message)
	// Completed reports are retried independently of the agent. Its token and
	// data may already be gone; this callback can only update this one job.
	if s.CallbackURL != "" {
		for attempt := 0; attempt < 30; attempt++ {
			if report(s, j) == nil {
				break
			}
			select {
			case <-ctx.Done():
				attempt = 30
			case <-time.After(10 * time.Second):
			}
		}
	}
	// Keep the safe result and root-only diagnostic log. Remove copied code and
	// job-scoped credentials even when the application itself was purged.
	cleanup := []string{"request.json", "worker", "install.sh", "uninstall.sh", "agent.new"}
	// A successful health check does not authorize deleting the previous safe
	// binary. Keep recovery artifacts until an explicit retention policy applies.
	for _, name := range cleanup {
		_ = os.Remove(m.path(id, name))
	}
	return nil // execution outcome is persisted, not inferred from process exit
}

func command(ctx context.Context, log io.Writer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout, c.Stderr = log, log
	// Do not let daemon environment supply Bash startup code or alternate tools.
	c.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive"}
	return c.Run()
}

func (m *Manager) updateController(ctx context.Context, s Spec, log io.Writer, stage func(string, string, string)) (string, string, error) {
	if !repoPattern.MatchString(s.Repository) {
		return "failed", "安装来源无效", errors.New("invalid repository")
	}
	stage("running", "upgrade", "正在下载、校验、停服备份并升级控制端；页面会短暂断开")
	installer := m.path(s.ID, "install.sh")
	if err := os.WriteFile(installer, lifecycle.Install, 0700); err != nil {
		return "failed", "无法准备升级程序", err
	}
	err := command(ctx, log, "bash", installer, "--update", "--repo", s.Repository, "--version", s.Version, "--auto-rollback")
	if err == nil {
		return "succeeded", "控制端升级完成，启动健康检查通过", nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 20 {
		return "rolled_back", "升级失败，已恢复旧版本及升级前数据，健康检查通过", err
	}
	return "failed", "升级未完成；请查看维护日志及升级前备份", err
}

func (m *Manager) updateAgent(ctx context.Context, s Spec, log io.Writer, stage func(string, string, string)) (string, string, error) {
	const target = "/usr/local/bin/ctlvps-agent"
	lock, err := os.OpenFile("/run/lock/ctlvps-install.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "failed", "无法获取安装锁，原 agent 未更换", err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return "failed", "终端已有安装或卸载任务，原 agent 未更换", err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	stage("running", "download", "正在下载并校验控制端提供的 agent")
	newPath, oldPath := m.path(s.ID, "agent.new"), m.path(s.ID, "agent.previous")
	if err := downloadAgent(ctx, s.DownloadURL, s.SHA256, newPath); err != nil {
		return "failed", "下载或 SHA256 校验失败，原 agent 未更换", err
	}
	payload, err := os.Open(newPath)
	if err != nil {
		return "failed", "无法读取新程序", err
	}
	defer payload.Close()
	info, err := payload.Stat()
	if err != nil {
		return "failed", "无法读取新程序大小", err
	}
	if err = secureupdate.VerifyReader(ctx, "agent", "", payload); err != nil {
		return "failed", "官方发行校验失败，原程序未更换", err
	}
	if err := command(ctx, log, newPath, "version"); err != nil {
		return "failed", "新 agent 无法在本机运行，原 agent 未更换", err
	}
	if err := secureupdate.CheckAgentCompatibility(ctx, newPath, "/var/lib/ctlvps-agent/state.json"); err != nil {
		return "failed", "新 agent 不兼容已启用的网络或计量功能，原 agent 未更换", err
	}
	if info, err := os.Lstat(target); err != nil || !info.Mode().IsRegular() {
		return "failed", "agent 安装路径不是默认普通文件", errors.New("nonstandard agent path")
	}
	if err = diskbudget.Check(filepath.Dir(oldPath), info.Size()+8<<20, 16); err != nil {
		return "failed", "维护目录空间不足，原 agent 保持运行", err
	}
	if err := copyFile(target, oldPath, 0700); err != nil {
		return "failed", "备份 agent 失败，原程序未更换", err
	}
	reservation, err := diskbudget.Reserve(filepath.Dir(target), info.Size()*2)
	if err != nil {
		return "failed", "程序分区无法预留更新与恢复空间，原 agent 保持运行", err
	}
	defer func() {
		if reservation != nil {
			_ = diskbudget.Release(reservation)
		}
	}()
	recovery, err := secureupdate.PrepareAgentRecovery(oldPath)
	if err != nil {
		return "failed", "旧程序不是安全恢复点，原 agent 保持运行", err
	}
	if err = writeJSON(m.path(s.ID, "recovery.json"), recovery); err != nil {
		return "failed", "恢复事务无法持久化，原 agent 保持运行", err
	}
	if err = diskbudget.Release(reservation); err != nil {
		return "failed", "无法启用预留空间，原 agent 保持运行", err
	}
	reservation = nil
	stage("running", "restart", "正在替换并重启 agent，已部署服务继续运行")
	if err := command(ctx, log, "systemctl", "stop", "ctlvps-agent"); err != nil {
		return "failed", "停止 agent 失败，原程序未更换", err
	}
	// /var/lib and /usr/local may be different filesystems; stage beside target
	// before renaming atomically. Never overwrite a symlink left by an operator.
	err = installAgentFile(newPath, target)
	if err == nil {
		err = command(ctx, log, "systemctl", "start", "ctlvps-agent")
	}
	if err == nil {
		err = agentHealthy(ctx, s.SHA256)
	}
	if err == nil {
		return "succeeded", "agent 升级完成，运行文件校验通过", nil
	}
	_, _ = fmt.Fprintln(log, "new agent failed:", err)
	stage("running", "rollback", "新 agent 启动失败，正在恢复旧程序")
	_ = command(ctx, log, "systemctl", "stop", "ctlvps-agent")
	rollback := recovery.CheckLocal()
	if rollback == nil {
		rollback = installAgentFile(oldPath, target)
	}
	if rollback == nil {
		rollback = command(ctx, log, "systemctl", "start", "ctlvps-agent")
	}
	if rollback == nil {
		oldSHA, hashErr := hashFile(oldPath)
		if hashErr == nil {
			rollback = agentHealthy(ctx, oldSHA)
		} else {
			rollback = hashErr
		}
	}
	if rollback == nil {
		return "rolled_back", "agent 升级失败，已恢复旧程序并通过运行检查", err
	}
	return "failed", "agent 升级及自动恢复失败，请从服务器终端处理", rollback
}

func installAgentFile(from, target string) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".ctlvps-agent-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	src, err := os.Open(from)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, src)
	src.Close()
	if err == nil {
		err = tmp.Chmod(0755)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, target)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func agentHealthy(ctx context.Context, want string) error {
	stable := 0
	for attempt := 0; attempt < 30; attempt++ {
		pid, err := exec.CommandContext(ctx, "systemctl", "show", "ctlvps-agent", "-p", "MainPID", "--value").Output()
		p := strings.TrimSpace(string(pid))
		if err == nil && p != "0" && p != "" {
			if got, err := hashFile("/proc/" + p + "/exe"); err == nil && strings.EqualFold(got, want) {
				stable++
				if stable >= 5 {
					return nil
				}
			} else {
				stable = 0
			}
		} else {
			stable = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("agent process did not become healthy")
}

func downloadAgent(ctx context.Context, rawURL, want, path string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || !shaPattern.MatchString(want) {
		return errors.New("invalid agent download")
	}
	if err = diskbudget.Check(filepath.Dir(path), 128<<20, 4); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, err := downloadAgentTo(ctx, rawURL, io.MultiWriter(f, h))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 128<<20 || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return errors.New("agent checksum mismatch")
	}
	ok = true
	return nil
}

func report(s Spec, j Job) error {
	if s.CallbackURL == "" {
		return nil
	}
	u, err := url.Parse(s.CallbackURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("invalid report endpoint")
	}
	b, _ := json.Marshal(j)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", s.CallbackURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.CallbackToken)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		c.Transport = agentnet.Transport{}
	}
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("report connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("report HTTP %d", resp.StatusCode)
	}
	return nil
}

var downloadAgentTo = func(ctx context.Context, raw string, dst io.Writer) (int64, error) {
	return agentnet.DownloadTo(ctx, raw, 128<<20, true, dst)
}
