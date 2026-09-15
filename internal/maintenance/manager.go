package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// Manager owns root-only job directories. Only the API-facing summary crosses
// the Unix socket; worker inputs cannot be edited by the ctlvps service user.
type Manager struct {
	Dir        string
	Executable string
	Launch     func(string, string) error // test seam; receives fixed ID and copied executable
}

func NewManager() *Manager                     { return &Manager{Dir: Directory} }
func (m *Manager) path(id, file string) string { return filepath.Join(m.Dir, id, file) }

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(out)
}

func (m *Manager) Get(id string) (Job, error) {
	var j Job
	if !idPattern.MatchString(id) {
		return j, errors.New("任务编号无效")
	}
	err := readJSON(m.path(id, "status.json"), &j)
	return j, err
}

func (m *Manager) List() []Job {
	out := []Job{}
	entries, _ := os.ReadDir(m.Dir)
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		if j, err := m.Get(entry.Name()); err == nil {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Recover reports interrupted jobs after a reboot or a killed worker. It never
// silently repeats an uninstall. A running transient unit survives daemon restarts.
func (m *Manager) Recover() {
	for _, j := range m.List() {
		if !j.Active() || time.Since(j.UpdatedAt) < time.Minute {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit(j.ID)).Run() == nil
		cancel()
		if !active {
			j.Status, j.Stage, j.Message = "interrupted", "interrupted", "执行进程已中断，请检查本机维护日志后重新发起"
			j.UpdatedAt = time.Now().UTC()
			_ = writeJSON(m.path(j.ID, "status.json"), j)
		}
	}
}

func unit(id string) string { return "ctlvps-maintenance-job-" + id + ".service" }

func (m *Manager) Start(s Spec) (Job, error) {
	if err := s.Validate(); err != nil {
		return Job{}, err
	}
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return Job{}, err
	}
	// All roles on the same machine share admission control, including daemon
	// restarts. The scripts additionally serialize against terminal installations.
	lock, err := os.OpenFile(filepath.Join(m.Dir, "admission.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Job{}, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return Job{}, err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	m.Recover()
	if old, err := m.Get(s.ID); err == nil {
		if old.Request != s.Request {
			return Job{}, errors.New("任务编号已用于其他操作")
		}
		return old, nil
	}
	for _, j := range m.List() {
		if j.Active() {
			return Job{}, ErrBusy
		}
	}
	dir := filepath.Join(m.Dir, s.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return Job{}, err
	}
	j := Job{Request: s.Request, Status: "queued", Stage: "queued", Message: "任务已保存，等待执行", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := writeJSON(m.path(s.ID, "request.json"), s); err != nil {
		return Job{}, err
	}
	if err := writeJSON(m.path(s.ID, "status.json"), j); err != nil {
		return Job{}, err
	}
	exe := m.Executable
	if exe == "" {
		exe, err = os.Executable()
	}
	worker := m.path(s.ID, "worker")
	if err == nil {
		err = copyFile(exe, worker, 0700)
	}
	if err == nil {
		if m.Launch != nil {
			err = m.Launch(s.ID, worker)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// A separate service cgroup is essential: stopping the API/agent must
			// not kill this worker. Its binary lives outside both install trees.
			err = exec.CommandContext(ctx, "systemd-run", "--quiet", "--collect", "--unit="+unit(s.ID), "--property=Type=exec", "--property=UMask=0077", "--property=RuntimeMaxSec=45min", "--property=TimeoutStopSec=30", worker, "maintenance-worker", s.ID).Run()
		}
	}
	if err != nil {
		j.Status, j.Stage, j.Message = "failed", "dispatch", "无法启动独立维护进程，请检查 systemd 日志"
		_ = writeJSON(m.path(s.ID, "status.json"), j)
		return j, errors.New(j.Message)
	}
	return j, nil
}

func copyFile(from, to string, mode os.FileMode) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if err == nil {
		err = dst.Sync()
	}
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func Supported() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

// Entry is used by copied controller and agent executables before normal startup.
func Entry(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if args[0] != "maintenance-worker" && args[0] != "maintenance-serve" {
		return false, nil
	}
	if !Supported() {
		return true, fmt.Errorf("网页维护仅支持 root/systemd 默认安装")
	}
	if args[0] == "maintenance-serve" && len(args) == 1 {
		return true, Serve()
	}
	if args[0] == "maintenance-worker" && len(args) == 2 && idPattern.MatchString(args[1]) {
		return true, NewManager().Run(args[1])
	}
	return true, errors.New("维护命令参数无效")
}
