package maintenance

import (
	"context"
	"ctlvps/internal/networkconfig"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/openrc"
)

// The child acknowledges only after leaving the agent cgroup. Setsid alone
// cannot protect a worker from OpenRC's whole-cgroup stop.
func launchOpenRC(id, worker string) error {
	ready, signal, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ready.Close()
	cmd := exec.Command(worker, "maintenance-openrc-host", id)
	cmd.ExtraFiles = []*os.File{signal}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "GOMAXPROCS=2", "GOMEMLIMIT=24MiB"}
	err = cmd.Start()
	signal.Close()
	if err != nil {
		return err
	}
	_ = ready.SetReadDeadline(time.Now().Add(15 * time.Second))
	b := make([]byte, 1)
	n, err := ready.Read(b)
	if err != nil || n != 1 || b[0] != 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("OpenRC maintenance worker did not enter its independent cgroup")
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func openRCWorker(id string) error {
	signal := os.NewFile(3, "maintenance-ready")
	if signal == nil {
		return errors.New("missing maintenance readiness pipe")
	}
	if err := openrc.PrepareMaintenance(id); err != nil {
		signal.Close()
		return err
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		signal.Close()
		return err
	}
	signal.Close()
	return NewManager().Run(id)
}

func agentService(ctx context.Context, log interface{ Write([]byte) (int, error) }, action string) error {
	if openrc.Enabled() {
		const binary = "/usr/local/bin/ctlvps-agent"
		if err := command(ctx, log, binary, "openrc-verify", "ctlvps-agent.service"); err != nil {
			return err
		}
		// Launch outside the service-host: startup refresh may stop that host.
		if action == "start" {
			return command(ctx, log, binary, "openrc-agent-start")
		}
		if err := command(ctx, log, "rc-service", "ctlvps-agent", action); err != nil {
			return err
		}
		if action == "stop" {
			return command(ctx, log, binary, "openrc-stop", "ctlvps-agent.service")
		}
		return nil
	}
	return command(ctx, log, "systemctl", action, "ctlvps-agent")
}

func agentPID(ctx context.Context) (string, error) {
	if openrc.Enabled() {
		if err := openrc.AgentReady(); err != nil {
			return "", err
		}
		pid, err := openrc.MainPID("ctlvps-agent.service")
		return strconv.Itoa(pid), err
	}
	raw, err := exec.CommandContext(ctx, "systemctl", "show", "ctlvps-agent", "-p", "MainPID", "--value").Output()
	return string(raw), err
}

// Generate from the verified candidate so script semantics match its binary.
func installOpenRCScript(ctx context.Context, binary string) error {
	cmd := exec.CommandContext(ctx, binary, "openrc-script", "ctlvps-agent.service")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	script, err := cmd.Output()
	if err != nil {
		return err
	}
	if len(script) == 0 || len(script) > 32<<10 {
		return errors.New("invalid OpenRC agent script")
	}
	const target = "/etc/init.d/ctlvps-agent"
	if st, err := os.Lstat(target); err != nil || !st.Mode().IsRegular() {
		return errors.New("nonstandard OpenRC agent script")
	}
	tmp, err := os.CreateTemp("/etc/init.d", ".ctlvps-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(script); err == nil {
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
	return os.Rename(tmp.Name(), target)
}

func checkOpenRCMaintenanceCandidate(ctx context.Context, binary string) error {
	cmd := exec.CommandContext(ctx, binary, "capabilities")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	raw, err := cmd.Output()
	if err != nil || len(raw) > 4096 {
		return errors.New("candidate lacks OpenRC maintenance capabilities")
	}
	var c networkconfig.ExecutableCapabilities
	if json.Unmarshal(raw, &c) != nil || c.OpenRC != 1 || c.OpenRCMaintenance != 1 {
		return errors.New("candidate lacks OpenRC maintenance capabilities")
	}
	return nil
}

// A separate bounded supervisor survives agent stop and kills orphaned worker
// children on exit, OOM or deadline. Neither process lives in the agent tree.
func openRCHost(id string) error {
	signal := os.NewFile(3, "maintenance-host-ready")
	if signal == nil {
		return errors.New("missing maintenance host pipe")
	}
	defer signal.Close()
	if err := openrc.PrepareMaintenanceHost(id); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ready, childSignal, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ready.Close()
	cmd := exec.Command(exe, "maintenance-openrc-worker", id)
	cmd.ExtraFiles = []*os.File{childSignal}
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "GOMAXPROCS=2", "GOMEMLIMIT=" + agentbudget.WorkerGoLimit}
	if err = cmd.Start(); err != nil {
		childSignal.Close()
		return err
	}
	childSignal.Close()
	defer openrc.KillMaintenance(id)
	_ = ready.SetReadDeadline(time.Now().Add(10 * time.Second))
	b := make([]byte, 1)
	if n, e := ready.Read(b); e != nil || n != 1 || b[0] != 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("maintenance child did not become ready")
	}
	if _, err = signal.Write([]byte{1}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	signal.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(45 * time.Minute):
		_ = openrc.KillMaintenance(id)
		return <-done
	}
}
