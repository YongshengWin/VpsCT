//go:build linux

package openrc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"ctlvps/internal/secureupdate"
	"golang.org/x/sys/unix"
)

const runtimeDir = "/run/ctlvps-openrc"

func trustedDirectory(path string, create bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid managed directory")
	}
	cur := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		st, e := os.Lstat(cur)
		if os.IsNotExist(e) && create {
			if e = os.Mkdir(cur, 0755); e != nil {
				return e
			}
			st, e = os.Lstat(cur)
		}
		if e != nil {
			return e
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe managed directory %s", cur)
		}
	}
	return nil
}

func trustedExecutable(path string) error {
	if e := trustedDirectory(filepath.Dir(path), false); e != nil {
		return e
	}
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("unsafe executable %s", path)
	}
	return nil
}

// Alpine 3.21 keeps util-linux setpriv in /usr/bin while /bin/setpriv is
// a BusyBox applet link. Only accept the real, protected package executable.
func trustedSetpriv() (string, error) {
	for _, path := range []string{"/usr/bin/setpriv", "/bin/setpriv"} {
		if err := trustedExecutable(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", errors.New("util-linux setpriv executable is missing")
}

func CheckEnvironment(ctx context.Context) error {
	if !Enabled() || os.Geteuid() != 0 {
		return errors.New("OpenRC runtime requires root and a running OpenRC init")
	}
	for _, bin := range []string{"/usr/bin/bwrap"} {
		if e := trustedExecutable(bin); e != nil {
			return e
		}
	}
	if _, err := trustedSetpriv(); err != nil {
		return err
	}
	for _, bin := range []string{"rc-service", "rc-update", "supervise-daemon", "nft"} {
		p, e := exec.LookPath(bin)
		if e != nil {
			return e
		}
		if e = trustedExecutable(p); e != nil {
			return e
		}
	}
	b, e := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if e != nil {
		return errors.New("Alpine requires unified cgroup v2")
	}
	for _, controller := range []string{"memory", "pids"} {
		if !strings.Contains(" "+strings.TrimSpace(string(b))+" ", " "+controller+" ") {
			return fmt.Errorf("Alpine requires delegated cgroup %s controller", controller)
		}
	}
	if e = unix.Access("/sys/fs/cgroup/cgroup.subtree_control", unix.W_OK); e != nil {
		return fmt.Errorf("Alpine requires writable delegated cgroup v2: %w", e)
	}
	return ctx.Err()
}

func cgpath(group string) string { return filepath.Join("/sys/fs/cgroup", group) }

func controller(path string) error {
	file := filepath.Join(path, "cgroup.subtree_control")
	if e := os.WriteFile(file, []byte("+memory +pids"), 0600); e != nil {
		return fmt.Errorf("enable cgroup controllers: %w", e)
	}
	b, e := os.ReadFile(file)
	if e != nil {
		return e
	}
	for _, c := range []string{"memory", "pids"} {
		if !strings.Contains(" "+strings.TrimSpace(string(b))+" ", " "+c+" ") {
			return errors.New("cgroup controller was not enabled")
		}
	}
	return nil
}

func makeGroup(group string) error {
	if !strings.HasPrefix(group, groupRoot) || filepath.Clean(group) != group {
		return errors.New("invalid managed cgroup")
	}
	parent := "/sys/fs/cgroup"
	for _, part := range strings.Split(strings.TrimPrefix(group, "/"), "/") {
		if e := controller(parent); e != nil {
			return e
		}
		parent = filepath.Join(parent, part)
		if e := os.Mkdir(parent, 0755); e != nil && !os.IsExist(e) {
			return e
		}
		st, e := os.Lstat(parent)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe managed cgroup")
		}
	}
	return nil
}

func setLimit(path, key, value string) error {
	file := filepath.Join(path, key)
	if e := os.WriteFile(file, []byte(value), 0600); e != nil {
		return fmt.Errorf("write %s: %w", key, e)
	}
	raw, e := os.ReadFile(file)
	if e != nil || strings.TrimSpace(string(raw)) != value {
		return fmt.Errorf("cgroup %s limit did not take effect", key)
	}
	return nil
}

func limits(group string, memory, pids int) error {
	if e := makeGroup(group); e != nil {
		return e
	}
	path := cgpath(group)
	for _, kv := range []struct{ k, v string }{{"memory.max", strconv.FormatInt(int64(memory)<<20, 10)}, {"memory.high", strconv.FormatInt(int64(memory)*3/4<<20, 10)}, {"pids.max", strconv.Itoa(pids)}} {
		if e := setLimit(path, kv.k, kv.v); e != nil {
			return e
		}
	}
	return nil
}

func budget(body string) (int, error) {
	p, e := properties(body)
	if e != nil {
		return 0, e
	}
	max, e := strconv.Atoi(strings.TrimSuffix(p["MemoryMax"], "M"))
	if e != nil || !strings.HasSuffix(p["MemoryMax"], "M") || max < 16 || max > 65536 || p["MemoryAccounting"] != "yes" || p["MemoryHigh"] != strconv.Itoa(max*3/4)+"M" {
		return 0, errors.New("invalid OpenRC aggregate proxy budget")
	}
	return max, nil
}

func ensureSlice(unit string) error {
	g, e := cgroup(unit)
	if e != nil {
		return e
	}
	if _, ok := meterID(unit); ok {
		return EnsureMeter(unit)
	}
	if unit == "ctlvps-proxy.slice" {
		b, e := secureupdate.ReadProtectedConfig(filepath.Join(UnitDir, unit))
		if e != nil {
			return e
		}
		n, e := budget(string(b))
		if e != nil {
			return e
		}
		return limits(g, n, 512)
	}
	if unit == "ctlvps-proxy-public.slice" || unit == "ctlvps-proxy-private.slice" {
		if e = ensureSlice("ctlvps-proxy.slice"); e != nil {
			return e
		}
		return makeGroup(g)
	}
	return errors.New("unsupported OpenRC permission slice")
}

// Stop terminates the entire business cgroup. It never kills the supervisor,
// which must first be stopped by rc-service so a deliberate stop stays stopped.
func Stop(unit string) error {
	g, e := cgroup(unit)
	if e != nil {
		return e
	}
	path := cgpath(g)
	if _, e = os.Lstat(path); os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0600); e != nil {
		return fmt.Errorf("whole-cgroup kill unavailable: %w", e)
	}
	return waitEmpty(path)
}

func inGroup(pid int, group string) bool {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	return e == nil && strings.TrimSpace(string(b)) == "0::"+group
}

func populated(unit string) bool {
	g, e := cgroup(unit)
	if e != nil {
		return false
	}
	b, e := os.ReadFile(filepath.Join(cgpath(g), "cgroup.events"))
	if os.IsNotExist(e) {
		return false
	}
	if e != nil {
		return true
	}
	return !strings.Contains("\n"+string(b), "\npopulated 0\n")
}

func MainPID(unit string) (int, error) {
	g, e := cgroup(unit)
	if e != nil {
		return 0, e
	}
	if strings.HasSuffix(unit, ".slice") {
		return 0, nil
	}
	b, e := os.ReadFile(filepath.Join(cgpath(g), "cgroup.procs"))
	if os.IsNotExist(e) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	want := launcher
	var argv []string
	if port, ok := mitaPort(unit); ok {
		want = "/opt/ctlvps/bin/mita"
		argv = []string{want, "run"}
		_ = port
	} else if unit == "ctlvps-singbox.service" || unit == "ctlvps-singbox-private.service" {
		p, e := readUnit(unit)
		if e != nil {
			return 0, e
		}
		want = "/opt/ctlvps/bin/sing-box"
		argv = []string{want, "run", "-c", "/etc/ctlvps-proxy/" + p.profile + "/config.json"}
	} else if unit == "ctlvps-proxy-guard.service" {
		argv = []string{want, "proxy-guard-loop"}
	} else if unit == "ctlvps-agent.service" {
		argv = []string{want, "run", "--state", "/var/lib/ctlvps-agent"}
	} else if unit == "ctlvps-service-host.service" {
		argv = []string{want, "openrc-manager"}
	} else {
		return 0, nil
	}
	for _, field := range strings.Fields(string(b)) {
		pid, e := strconv.Atoi(field)
		if e != nil || pid <= 0 || !inGroup(pid, g) {
			continue
		}
		exe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if e != nil || (exe != want && exe != want+" (deleted)") {
			continue
		}
		cmd, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if e != nil || string(cmd) != strings.Join(argv, "\x00")+"\x00" {
			continue
		}
		return pid, nil
	}
	return 0, nil
}

func helperCurrent(unit string) (bool, error) {
	pid, err := MainPID(unit)
	if err != nil {
		return false, err
	}
	if pid == 0 {
		if populated(unit) {
			return false, errors.New("OpenRC helper cgroup has no trusted main process")
		}
		return true, nil
	}
	if err := trustedExecutable(launcher); err != nil {
		return false, err
	}
	want, err := os.Stat(launcher)
	if err != nil {
		return false, err
	}
	running, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false, err
	}
	return os.SameFile(want, running), nil
}

func processStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// comm can contain spaces and parentheses; fields after its final ')' start
	// at field 3, so starttime (field 22) is index 19 of the remaining fields.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return "", errors.New("invalid OpenRC process stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return "", errors.New("incomplete OpenRC process stat")
	}
	if n, err := strconv.ParseUint(f[19], 10, 64); err != nil || n == 0 {
		return "", errors.New("invalid OpenRC process start time")
	}
	return f[19], nil
}

func VerifyProxy(unit, command string) error {
	p, e := readUnit(unit)
	if e != nil {
		return e
	}
	if p.command != command {
		return errors.New("OpenRC proxy command differs")
	}
	pid, e := MainPID(unit)
	if e != nil || pid == 0 {
		return errors.New("OpenRC proxy identity unavailable")
	}
	mnt, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if e != nil {
		return e
	}
	ours, e := os.Readlink("/proc/self/ns/mnt")
	if e != nil || mnt == ours {
		return errors.New("OpenRC proxy mount namespace is not isolated")
	}
	info, e := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", pid))
	if e != nil {
		return e
	}
	readOnlyRoot := false
	for _, line := range strings.Split(string(info), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 5 && fields[4] == "/" {
			for _, flag := range strings.Split(fields[5], ",") {
				readOnlyRoot = readOnlyRoot || flag == "ro"
			}
		}
	}
	if !readOnlyRoot {
		return errors.New("OpenRC proxy root is writable")
	}
	return nil
}

func Exec(unit string) error {
	if os.Geteuid() != 0 || !Enabled() {
		return errors.New("OpenRC launcher requires root")
	}
	if e := CheckEnvironment(context.Background()); e != nil {
		return e
	}
	if e := trustedExecutable(launcher); e != nil {
		return e
	}
	g, e := cgroup(unit)
	if e != nil {
		return e
	}
	if unit == "ctlvps-agent.service" || unit == "ctlvps-proxy-guard.service" || unit == "ctlvps-service-host.service" {
		if e = Verify(unit); e != nil {
			return e
		}
		memory, pids := 128, 128
		goMemory := "64MiB"
		args := []string{launcher, "run", "--state", "/var/lib/ctlvps-agent"}
		if unit == "ctlvps-proxy-guard.service" {
			memory = 48
			pids = 64
			goMemory = "24MiB"
			args = []string{launcher, "proxy-guard-loop"}
		}
		if unit == "ctlvps-service-host.service" {
			memory = 64
			pids = 128
			goMemory = "24MiB"
			args = []string{launcher, "openrc-manager"}
		}
		if e = limits(g, memory, pids); e != nil {
			return e
		}
		if e = join(g); e != nil {
			return e
		}
		if unit == "ctlvps-proxy-guard.service" {
			return guardSandbox(goMemory)
		}
		if e = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); e != nil {
			return e
		}
		return unix.Exec(launcher, args, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "GOMEMLIMIT=" + goMemory, "GOMAXPROCS=2"})
	}
	p, e := readUnit(unit)
	if e != nil {
		return e
	}
	if !p.check {
		if e = Verify(unit); e != nil {
			return e
		}
		if e = ensureSlice(p.slice); e != nil {
			return e
		}
	} else {
		if e = os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0600); e != nil {
			return e
		}
	}
	if e = limits(g, p.memory, 128); e != nil {
		return e
	}
	if e = join(g); e != nil {
		return e
	}
	if e = trustedDirectory(runtimeDir, true); e != nil {
		return e
	}
	empty := runtimeDir + "/empty"
	if e = trustedDirectory(empty, true); e != nil {
		return e
	}
	if e = os.Chmod(empty, 0000); e != nil {
		return e
	}
	identity, e := user.Lookup(p.account)
	if e != nil {
		return e
	}
	if identity.Uid == "0" || identity.Gid == "0" {
		return errors.New("OpenRC proxy identity must be unprivileged")
	}
	for _, path := range []string{"/etc/ctlvps", "/var/lib/ctlvps-agent", "/var/lib/ctlvps-security", "/var/lib/ctlvps-maintenance", "/run/ctlvps-maintenance", "/root", "/home"} {
		if e = trustedDirectory(path, true); e != nil {
			return e
		}
	}
	binary := "/opt/ctlvps/bin/sing-box"
	if _, ok := mitaPort(unit); ok {
		binary = "/opt/ctlvps/bin/mita"
	}
	if e = trustedExecutable(binary); e != nil {
		return e
	}
	_, mita := mitaPort(unit)
	if !mita {
		if e = trustedDirectory(filepath.Dir(p.log), false); e != nil {
			return e
		}
		st, e := os.Lstat(p.log)
		if e != nil || !st.Mode().IsRegular() {
			return errors.New("invalid OpenRC proxy log")
		}
	}
	// bwrap creates the namespace as root; setpriv drops UID, groups and all
	// capabilities except the two required by the official sing-box process.
	argv := []string{"bwrap", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/var/tmp"}
	if !mita {
		argv = append(argv, "--bind", p.log, p.log)
	} else {
		// mita binds its private RPC socket after dropping UID.
		argv = append(argv, "--chmod", "1777", "/tmp", "--chmod", "1777", "/var/tmp")
	}
	if p.check {
		argv = append(argv, "--unshare-net")
	}
	for _, path := range []string{"/etc/ctlvps", "/var/lib/ctlvps-agent", "/var/lib/ctlvps-security", "/var/lib/ctlvps-maintenance", "/run/ctlvps-maintenance", "/root", "/home"} {
		argv = append(argv, "--ro-bind", empty, path)
	}
	if p.acmeSource != "" {
		if e = trustedDirectory(filepath.Dir(p.acmeSource), false); e != nil {
			return e
		}
		if e = trustedDirectory(filepath.Dir(p.acmeTarget), false); e != nil {
			return e
		}
		argv = append(argv, "--bind", p.acmeSource, p.acmeTarget)
	}
	caps := []string{"CAP_SETUID", "CAP_SETGID", "CAP_SETPCAP", "CAP_NET_BIND_SERVICE"}
	capSpec := "-all,+net_bind_service"
	if _, ok := mitaPort(unit); !ok {
		caps = append(caps, "CAP_NET_RAW")
		capSpec += ",+net_raw"
	}
	for _, cap := range caps {
		argv = append(argv, "--cap-add", cap)
	}
	setpriv, e := trustedSetpriv()
	if e != nil {
		return e
	}
	argv = append(argv, "--", setpriv, "--reuid="+identity.Uid, "--regid="+identity.Gid, "--clear-groups", "--bounding-set="+capSpec, "--inh-caps="+capSpec, "--ambient-caps="+capSpec, "--no-new-privs", launcher)
	argv = append(argv, strings.Fields(strings.TrimPrefix(p.command, launcher+" "))...)
	if e = unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: uint64(p.nofile), Max: uint64(p.nofile)}); e != nil {
		return e
	}
	unix.Umask(0077)
	return unix.Exec("/usr/bin/bwrap", argv, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "GOMEMLIMIT=" + strconv.Itoa(p.goMemory) + "MiB", "GOMAXPROCS=2"})
}

func join(group string) error {
	// cgroup.procs migrates the whole process, including every Go runtime thread.
	runtime.LockOSThread()
	if e := os.WriteFile(filepath.Join(cgpath(group), "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0600); e != nil {
		return e
	}
	if !inGroup(os.Getpid(), group) {
		return errors.New("managed process did not enter its cgroup")
	}
	return nil
}

func guardSandbox(goMemory string) error {
	if e := trustedDirectory(runtimeDir, true); e != nil {
		return e
	}
	empty := runtimeDir + "/empty"
	if e := trustedDirectory(empty, true); e != nil {
		return e
	}
	if e := os.Chmod(empty, 0000); e != nil {
		return e
	}
	if e := trustedDirectory("/run/ctlvps-proxy", true); e != nil {
		return e
	}
	args := []string{"bwrap", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/var/tmp", "--bind", "/run/ctlvps-proxy", "/run/ctlvps-proxy"}
	for _, path := range []string{"/etc/ctlvps", "/var/lib/ctlvps-agent", "/var/lib/ctlvps-security", "/var/lib/ctlvps-maintenance", "/run/ctlvps-maintenance", "/root", "/home"} {
		if e := trustedDirectory(path, true); e != nil {
			return e
		}
		args = append(args, "--ro-bind", empty, path)
	}
	setpriv, e := trustedSetpriv()
	if e != nil {
		return e
	}
	args = append(args, "--cap-add", "CAP_SETPCAP", "--cap-add", "CAP_NET_ADMIN", "--", setpriv, "--bounding-set=-all,+net_admin", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs", launcher, "proxy-guard-loop")
	return unix.Exec("/usr/bin/bwrap", args, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "GOMEMLIMIT=" + goMemory, "GOMAXPROCS=2"})
}
