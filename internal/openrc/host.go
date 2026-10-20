package openrc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/boundedexec"
	"ctlvps/internal/secureupdate"
)

func Enabled() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, e := os.Stat("/run/systemd/system"); e == nil {
		return false
	}
	_, e := os.Stat("/run/openrc/softlevel")
	return e == nil
}

func Available(ctx context.Context) bool { return CheckEnvironment(ctx) == nil }

func readMitaPlan(unit string) (plan, error) {
	b, err := secureupdate.ReadProtectedConfig(filepath.Join(UnitDir, unit))
	if err != nil {
		return plan{}, err
	}
	return parsePlan(unit, string(b))
}

func readUnit(unit string) (plan, error) {
	if _, e := cgroup(unit); e != nil {
		return plan{}, e
	}
	b, e := secureupdate.ReadProtectedConfig(filepath.Join(UnitDir, unit))
	if e != nil {
		return plan{}, e
	}
	return parsePlan(unit, string(b))
}

func writeProtected(path string, data []byte, mode os.FileMode) (bool, error) {
	if e := trustedDirectory(filepath.Dir(path), true); e != nil {
		return false, e
	}
	if old, e := secureupdate.ReadProtectedConfig(path); e == nil {
		if bytes.Equal(old, data) {
			st, e := os.Stat(path)
			if e != nil || st.Mode().Perm() != mode {
				return false, errors.New("managed file mode differs")
			}
			return false, nil
		}
	} else if !os.IsNotExist(e) {
		return false, e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".ctlvps-openrc-")
	if e != nil {
		return false, e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return false, e
	}
	if ce != nil {
		return false, ce
	}
	if e = os.Rename(tmp, path); e != nil {
		return false, e
	}
	return true, nil
}

func InstallUnit(name, body string) (bool, error) {
	if _, ok := meterID(name); ok {
		p, err := properties(body)
		if err != nil || len(p) != 2 || p["Description"] != "VpsCT node accounting" || p["IPAccounting"] != "yes" {
			return false, errors.New("invalid OpenRC meter slice")
		}
		return writeProtected(filepath.Join(UnitDir, name), []byte(body), 0644)
	}
	if strings.HasSuffix(name, ".slice") {
		if _, e := cgroup(name); e != nil {
			return false, e
		}
		p, e := properties(body)
		if e != nil {
			return false, e
		}
		for k := range p {
			if k != "Description" && k != "MemoryAccounting" && k != "MemoryHigh" && k != "MemoryMax" {
				return false, fmt.Errorf("unsupported OpenRC slice property %s", k)
			}
		}
		if _, present := p["Description"]; name != "ctlvps-proxy.slice" && (len(p) != 1 || !present) {
			return false, errors.New("permission slice has unexpected limits")
		}
		if name == "ctlvps-proxy.slice" {
			if _, e = budget(body); e != nil {
				return false, e
			}
		}
		return writeProtected(filepath.Join(UnitDir, name), []byte(body), 0644)
	}
	if _, e := parsePlan(name, body); e != nil {
		return false, e
	}
	changed, e := writeProtected(filepath.Join(UnitDir, name), []byte(body), 0644)
	if e != nil {
		return false, e
	}
	if strings.HasPrefix(name, "ctlvps-proxy-check-") {
		return changed, nil
	}
	c, e := installScript(name)
	return changed || c, e
}

func installScript(unit string) (bool, error) {
	script, e := Script(unit)
	if e != nil {
		return false, e
	}
	name, _ := serviceName(unit)
	if _, e = os.Lstat("/etc/conf.d/" + name); !os.IsNotExist(e) {
		return false, errors.New("OpenRC per-service overrides are not supported")
	}
	return writeProtected("/etc/init.d/"+name, []byte(script), 0755)
}

func Verify(unit string) error {
	script, e := Script(unit)
	if e != nil {
		return e
	}
	name, _ := serviceName(unit)
	if _, e = os.Lstat("/etc/conf.d/" + name); !os.IsNotExist(e) {
		return errors.New("OpenRC per-service overrides are not supported")
	}
	b, e := secureupdate.ReadProtectedConfig("/etc/init.d/" + name)
	if e != nil {
		return e
	}
	if !bytes.Equal(b, []byte(script)) {
		return errors.New("OpenRC service differs from the fixed VpsCT definition")
	}
	return nil
}

func Sync() error {
	entries, e := os.ReadDir(UnitDir)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "ctlvps-proxy-guard.service" || name == "ctlvps-service-host.service" {
			if _, e = installScript(name); e != nil {
				return e
			}
			continue
		}
		if _, e = serviceName(name); e != nil || name == "ctlvps-agent.service" {
			continue
		}
		if _, e = readUnit(name); e != nil {
			return e
		}
		if _, e = installScript(name); e != nil {
			return e
		}
	}
	return nil
}

func EnsureGuard(ctx context.Context, executable string) error {
	if executable != launcher {
		return errors.New("invalid OpenRC guard launcher")
	}
	if err := ensureManager(ctx); err != nil {
		return err
	}
	if _, e := writeProtected(filepath.Join(UnitDir, "ctlvps-proxy-guard.service"), []byte("# Managed by VpsCT (OpenRC v1)\n"), 0644); e != nil {
		return e
	}
	if _, e := installScript("ctlvps-proxy-guard.service"); e != nil {
		return e
	}
	if _, e := Control(ctx, "enable", "ctlvps-proxy-guard.service"); e != nil {
		return e
	}
	if _, e := Control(ctx, "is-active", "ctlvps-proxy-guard.service"); e == nil {
		return nil
	}
	_, e := Control(ctx, "start", "ctlvps-proxy-guard.service")
	return e
}

func invoke(ctx context.Context, bin string, args ...string) (string, error) {
	out, stderr, e := boundedexec.Run(ctx, "", 1<<20, bin, args...)
	if e != nil {
		return "", fmt.Errorf("%s: %w: %s", filepath.Base(bin), e, strings.TrimSpace(stderr))
	}
	return string(out), nil
}

// Control is the narrow systemctl compatibility surface used by the existing
// driver/recovery code. It accepts fixed identities, never arbitrary commands.
func Control(ctx context.Context, args ...string) (string, error) {
	if _, err := os.Lstat(managerSocket); err == nil {
		return managedControl(ctx, args)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return controlLocal(ctx, args...)
}

func controlLocal(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing OpenRC operation")
	}
	op := args[0]
	args = args[1:]
	if op == "daemon-reload" {
		return "", Sync()
	}
	if op == "list-units" {
		entries, e := os.ReadDir(UnitDir)
		if os.IsNotExist(e) {
			entries = nil
			e = nil
		}
		if e != nil {
			return "", e
		}
		patterns := []string{}
		active := false
		for _, a := range args {
			if strings.HasPrefix(a, "--state=") && strings.Contains(a, "active") {
				active = true
			}
			if !strings.HasPrefix(a, "-") {
				patterns = append(patterns, a)
			}
		}
		inventory := map[string]bool{}
		for _, entry := range entries {
			inventory[entry.Name()] = true
		}
		for _, name := range []string{"ctlvps-agent.service", "ctlvps-service-host.service", "ctlvps-proxy-guard.service", "ctlvps-singbox.service", "ctlvps-singbox-private.service"} {
			if populated(name) {
				inventory[name] = true
			}
		}
		var names []string
		for name := range inventory {
			if _, e := serviceName(name); e != nil {
				continue
			}
			matched := len(patterns) == 0
			for _, p := range patterns {
				ok, _ := filepath.Match(p, name)
				matched = matched || ok
			}
			if !matched {
				continue
			}
			running := populated(name)
			if active && !running {
				continue
			}
			state := "inactive"
			if running {
				state = "active"
			}
			names = append(names, name+" loaded "+state)
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	}
	if op == "show" {
		return show(args)
	}
	units := []string{}
	now := false
	for _, a := range args {
		if a == "--now" {
			now = true
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if _, e := cgroup(a); e != nil {
			return "", e
		}
		units = append(units, a)
	}
	if len(units) == 0 {
		return "", errors.New("missing OpenRC service identity")
	}
	for _, u := range units {
		if strings.HasSuffix(u, ".slice") {
			if _, ok := meterID(u); ok && op == "is-active" {
				if _, _, _, e := ReadMeter(u); e != nil {
					return "inactive\n", e
				}
				return "active\n", nil
			}
			if _, ok := meterID(u); ok && op == "stop" {
				if e := RemoveMeter(u); e != nil {
					return "", e
				}
				continue
			}
			if op != "start" && op != "stop" {
				return "", errors.New("unsupported OpenRC slice operation")
			}
			if op == "start" {
				if e := ensureSlice(u); e != nil {
					return "", e
				}
			}
			continue
		}
		name, e := serviceName(u)
		if e != nil {
			return "", e
		}
		if op == "is-active" {
			if pid, err := MainPID(u); err != nil {
				return "", err
			} else if pid > 0 {
				return "active\n", nil
			}
			return "inactive\n", errors.New("OpenRC service inactive")
		}
		if op == "is-enabled" {
			st, e := os.Lstat("/etc/runlevels/default/" + name)
			if e == nil && st.Mode()&os.ModeSymlink != 0 {
				target, e := os.Readlink("/etc/runlevels/default/" + name)
				if e == nil && target == "/etc/init.d/"+name {
					return "enabled\n", nil
				}
			}
			return "disabled\n", errors.New("OpenRC service disabled")
		}
		// Revoking a fixed business cgroup executes no service script. It must
		// remain possible after an interrupted write or a tampered init file.
		if op == "kill" {
			if e = Stop(u); e != nil {
				return "", e
			}
			continue
		}
		if _, e := os.Stat("/etc/init.d/" + name); os.IsNotExist(e) && (op == "stop" || op == "disable" || op == "kill") {
			if e = Stop(u); e != nil {
				return "", e
			}
			continue
		}
		if e = Verify(u); e != nil {
			if op == "stop" {
				e = errors.Join(e, Stop(u))
			}
			return "", e
		}
		switch op {
		case "enable":
			if _, e = invoke(ctx, "/sbin/rc-update", "add", name, "default"); e == nil && now {
				_, e = invoke(ctx, "/sbin/rc-service", name, "start")
			}
		case "disable":
			if _, e = os.Lstat("/etc/runlevels/default/" + name); os.IsNotExist(e) {
				e = nil
			} else {
				_, e = invoke(ctx, "/sbin/rc-update", "del", name, "default")
			}
		case "kill":
			e = Stop(u)
		case "stop":
			// Stop the supervisor first so it cannot respawn after cgroup.kill.
			if _, e = invoke(ctx, "/sbin/rc-service", name, "status"); e == nil {
				done := make(chan error, 1)
				go func() { _, err := invoke(ctx, "/sbin/rc-service", name, "stop"); done <- err }()
				killErr := Stop(u)
				e = errors.Join(killErr, <-done)
			} else {
				e = nil
			}
			if e == nil {
				e = Stop(u)
			}
		case "start", "restart", "reload-or-restart":
			action := "start"
			if op != "start" {
				action = "restart"
			}
			_, e = invoke(ctx, "/sbin/rc-service", name, action)
		default:
			return "", errors.New("unsupported OpenRC operation")
		}
		if e != nil {
			return "", e
		}
	}
	return "", nil
}

func show(args []string) (string, error) {
	var units []string
	var props []string
	value := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--value" {
			value = true
			continue
		}
		if a == "-p" && i+1 < len(args) {
			i++
			props = append(props, strings.Split(args[i], ",")...)
			continue
		}
		if strings.HasPrefix(a, "--property=") {
			props = append(props, strings.Split(strings.TrimPrefix(a, "--property="), ",")...)
			continue
		}
		if strings.HasPrefix(a, "-") {
			return "", errors.New("unsupported OpenRC show option")
		}
		if _, e := cgroup(a); e != nil {
			return "", e
		}
		units = append(units, a)
	}
	if len(props) == 0 || len(units) == 0 {
		return "", errors.New("missing OpenRC properties")
	}
	var blocks []string
	for _, u := range units {
		g, _ := cgroup(u)
		pid, err := MainPID(u)
		if err != nil {
			return "", err
		}
		state := "inactive"
		if pid > 0 {
			state = "active"
		}
		data := map[string]string{"Id": u, "ActiveState": state, "NRestarts": "0", "MemoryCurrent": "0", "ExecMainStartTimestamp": "", "Result": "success", "CPUUsageNSec": "0", "MainPID": strconv.Itoa(pid), "ControlGroup": g, "IPIngressBytes": "[not set]", "IPEgressBytes": "[not set]", "InvocationID": ""}
		if _, ok := meterID(u); ok {
			rx, tx, epoch, err := ReadMeter(u)
			if err != nil {
				return "", err
			}
			data["ActiveState"] = "active"
			data["IPIngressBytes"], data["IPEgressBytes"], data["InvocationID"] = strconv.FormatInt(rx, 10), strconv.FormatInt(tx, 10), epoch
		}
		if !strings.HasSuffix(u, ".slice") {
			if p, e := readUnit(u); e == nil {
				data["Slice"] = p.slice
				data["ExecStart"] = "{ argv[]=" + p.command + " ; }"
			}
		}
		if raw, e := os.ReadFile(filepath.Join("/sys/fs/cgroup", g, "memory.current")); e == nil {
			data["MemoryCurrent"] = strings.TrimSpace(string(raw))
		}
		if pid > 0 {
			if st, e := os.Stat(fmt.Sprintf("/proc/%d", pid)); e == nil {
				data["ExecMainStartTimestamp"] = st.ModTime().UTC().Format("Mon 2006-01-02 15:04:05 MST")
			}
		}
		var lines []string
		for _, k := range props {
			v, ok := data[k]
			if !ok {
				return "", fmt.Errorf("unsupported OpenRC property %s", k)
			}
			if value {
				lines = append(lines, v)
			} else {
				lines = append(lines, k+"="+v)
			}
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n") + "\n", nil
}

func JournalTail(unit string, n int) []string {
	name, e := serviceName(unit)
	if e != nil || n <= 0 {
		return nil
	}
	path := "/var/log/messages"
	if p, err := readUnit(unit); err == nil {
		path = p.log
	}
	f, e := os.Open(path)
	if e != nil {
		return nil
	}
	defer f.Close()
	if st, e := f.Stat(); e == nil && st.Size() > 256<<10 {
		_, _ = f.Seek(-(256 << 10), io.SeekEnd)
	}
	b, e := io.ReadAll(io.LimitReader(f, 256<<10))
	if e != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if path == "/var/log/messages" {
		filtered := []string{}
		for _, line := range lines {
			if strings.Contains(line, name+":") {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func Check(ctx context.Context, name, body string) error {
	p, e := parsePlan(name, body)
	if e != nil || !p.check {
		return errors.Join(e, errors.New("invalid OpenRC check identity"))
	}
	if _, e = InstallUnit(name, body); e != nil {
		return e
	}
	defer os.Remove(filepath.Join(UnitDir, name))
	defer Stop(name)
	out, stderr, e := boundedexec.Run(ctx, "", 16<<10, launcher, "openrc-exec", name)
	if e != nil {
		return fmt.Errorf("isolated OpenRC proxy preflight: %w: %s %s", e, strings.TrimSpace(stderr), strings.TrimSpace(string(out)))
	}
	return nil
}

// Keep the timeout at the caller while waiting for a populated cgroup to empty.
func waitEmpty(path string) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, e := os.ReadFile(filepath.Join(path, "cgroup.events"))
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if strings.Contains("\n"+string(b), "\npopulated 0\n") {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("OpenRC managed cgroup remains populated")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
