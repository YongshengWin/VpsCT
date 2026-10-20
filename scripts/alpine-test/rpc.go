//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"ctlvps/internal/openrc"
)

// Run before the external client starts, while the fixture's process budget is
// low. These probes only use the disposable container's fixed managed paths.
func verifyRPCAndGuard(ctx context.Context) {
	const socket = "/run/ctlvps-openrc/manage.sock"
	st, err := os.Lstat(socket)
	must(err)
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0600 {
		panic("service-host RPC is not a root-owned 0600 Unix socket")
	}
	identity, err := user.Lookup("ctlvps-sb")
	must(err)
	if identity.Uid == "0" || identity.Gid == "0" {
		panic("RPC probe identity is privileged")
	}
	probe := func(expectConnect bool) {
		// Chmod alone is not the authority: with DAC deliberately relaxed for
		// this isolated probe, SO_PEERCRED must still reject the proxy's UID.
		connected := "0"
		if expectConnect {
			connected = "1"
		}
		body := `import errno, json, socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(2)
try:
    s.connect(sys.argv[1])
except OSError as error:
    if sys.argv[2] == "0" and error.errno == errno.EACCES:
        sys.exit(0)
    raise
if sys.argv[2] == "0":
    raise RuntimeError("proxy UID connected to root-only RPC")
try:
    s.sendall(json.dumps(["daemon-reload"]).encode() + b"\n")
    reply = s.recv(4096)
except (BrokenPipeError, ConnectionResetError):
    reply = b""
if reply:
    raise RuntimeError("service-host accepted non-root peer credentials")
`
		cmd := exec.CommandContext(ctx, "/bin/setpriv", "--reuid="+identity.Uid, "--regid="+identity.Gid, "--clear-groups", "--no-new-privs", "/usr/bin/python3", "-c", body, socket, connected)
		if out, err := cmd.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("proxy RPC rejection probe failed: %v: %s", err, out))
		}
	}
	probe(false)
	func() {
		must(os.Chmod(socket, 0666))
		defer func() { must(os.Chmod(socket, 0600)) }()
		probe(true)
	}()
	for _, args := range [][]string{
		{"start", "sshd.service"},
		{"start", "ctlvps-singbox.service;id"},
		{"start", "ctlvps-singbox.service.d/override.conf"},
		{"start", "ctlvps-proxy-check-public.service"},
		{"exec", "/bin/sh"},
	} {
		if _, err := openrc.Control(ctx, args...); err == nil {
			panic("service-host RPC accepted a foreign command or identity: " + strings.Join(args, " "))
		}
	}
	guardPID, err := openrc.MainPID("ctlvps-proxy-guard.service")
	must(err)
	if guardPID == 0 {
		panic("independent OpenRC guard is absent")
	}
	guard := procFields(guardPID)
	if guard["Uid"] != "0\t0\t0\t0" || guard["NoNewPrivs"] != "1" || guard["Seccomp"] != "2" {
		panic("guard root identity, NoNewPrivileges or seccomp differs")
	}
	for _, key := range []string{"CapEff", "CapPrm", "CapBnd"} {
		caps, err := strconv.ParseUint(guard[key], 16, 64)
		must(err)
		if caps != 1<<12 { // CAP_NET_ADMIN, without service-management capabilities.
			panic("guard has unexpected capabilities: " + key + "=" + guard[key])
		}
	}
	ambient, err := strconv.ParseUint(guard["CapAmb"], 16, 64)
	must(err)
	if ambient != 0 {
		panic("guard retained ambient capabilities")
	}
	for _, ns := range []string{"mnt", "ipc", "uts"} {
		ours, err := os.Readlink("/proc/self/ns/" + ns)
		must(err)
		other, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", guardPID, ns))
		must(err)
		if ours == other {
			panic("guard namespace is shared: " + ns)
		}
	}
	info, err := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", guardPID))
	must(err)
	readOnlyRoot := false
	for _, line := range strings.Split(string(info), "\n") {
		f := strings.Fields(line)
		if len(f) > 5 && f[4] == "/" {
			for _, flag := range strings.Split(f[5], ",") {
				readOnlyRoot = readOnlyRoot || flag == "ro"
			}
		}
	}
	if !readOnlyRoot {
		panic("guard root mount is writable")
	}
	managerPID, err := openrc.MainPID("ctlvps-service-host.service")
	must(err)
	if managerPID == 0 || procFields(managerPID)["Uid"] != "0\t0\t0\t0" {
		panic("service-host root process is absent")
	}
	verifyKillWithUntrustedInit(ctx)
}

func procFields(pid int) map[string]string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	must(err)
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	return fields
}

func verifyKillWithUntrustedInit(ctx context.Context) {
	const unit = "ctlvps-singbox.service"
	const script = "/etc/init.d/ctlvps-singbox"
	const sentinel = "/tmp/ctlvps-untrusted-init-sentinel"
	must(openrc.Verify(unit))
	original, err := os.ReadFile(script)
	must(err)
	defer func() { must(os.WriteFile(script, original, 0755)) }()
	if err := os.Remove(sentinel); err != nil && !os.IsNotExist(err) {
		must(err)
	}
	// A partial logical/script update or unexpected init drift must not deny
	// the final fixed-cgroup kill. The appended shell line must never execute.
	bad := append(append([]byte{}, original...), []byte("\n: > "+sentinel+"\n")...)
	must(os.WriteFile(script, bad, 0755))
	if err := openrc.Verify(unit); err == nil {
		panic("mismatched init script was considered trusted")
	}
	_, err = openrc.Control(ctx, "kill", "--kill-who=all", "--signal=SIGKILL", unit)
	must(err)
	group := "/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice/" + unit
	eventually("kill despite untrusted init", func() bool {
		b, err := os.ReadFile(group + "/cgroup.events")
		return err == nil && strings.Contains(string(b), "populated 0")
	})
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		panic("kill executed the untrusted init script")
	}
	must(os.WriteFile(script, original, 0755))
	must(openrc.Verify(unit))
	// Restore trust before using any OpenRC script operation. Deliberately
	// stop the existing supervisor, then prove the fixed service starts again.
	_, err = openrc.Control(ctx, "stop", unit)
	must(err)
	_, err = openrc.Control(ctx, "start", unit)
	must(err)
	eventually("proxy after fixed init restoration", func() bool {
		pid, err := openrc.MainPID(unit)
		return err == nil && pid > 0
	})
}
