//go:build linux

package proxysandbox

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Install synchronizes the filter across Go runtime threads before exec.
// Architecture checking also rejects compat socketcall/x32 bypasses.
func Install() error {
	return install(false)
}

// InstallGuard keeps the watchdog's service RPC and nft netlink access while
// preserving its systemd restriction against Internet and other socket families.
func InstallGuard() error {
	return install(true)
}

func install(guard bool) error {
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return fmt.Errorf("unsupported proxy sandbox architecture")
	}
	ld := func(off uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: off}
	}
	jeq := func(k uint32, yes, no uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: k, Jt: yes, Jf: no}
	}
	ret := func(k uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: k} }
	f := []unix.SockFilter{
		ld(4), jeq(arch, 1, 0), ret(unix.SECCOMP_RET_KILL_PROCESS),
		ld(0),
		jeq(uint32(unix.SYS_IO_URING_SETUP), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		jeq(uint32(unix.SYS_IO_URING_ENTER), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		jeq(uint32(unix.SYS_IO_URING_REGISTER), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		// OpenRC supplies the mount sandbox directly. Prevent the untrusted
		// process from creating or joining another namespace after launch.
		jeq(uint32(unix.SYS_UNSHARE), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		jeq(uint32(unix.SYS_SETNS), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		// clone3 carries flags behind a pointer which classic seccomp cannot
		// inspect. ENOSYS lets libc use clone for ordinary thread creation.
		jeq(uint32(unix.SYS_CLONE3), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)),
		jeq(uint32(unix.SYS_CLONE), 0, 3), ld(16),
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_NEWCGROUP | unix.CLONE_NEWIPC | unix.CLONE_NEWNET | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWUSER | unix.CLONE_NEWUTS, Jt: 0, Jf: 1}, ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		ld(0),
		jeq(uint32(unix.SYS_PERSONALITY), 0, 1), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
		// x32 is not a supported ABI even though it uses AUDIT_ARCH_X86_64.
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jt: 0, Jf: 1}, ret(unix.SECCOMP_RET_KILL_PROCESS),
	}
	if guard {
		f = append(f,
			jeq(uint32(unix.SYS_SOCKET), 1, 0), jeq(uint32(unix.SYS_SOCKETPAIR), 0, 4),
			ld(16), jeq(unix.AF_UNIX, 2, 0), jeq(unix.AF_NETLINK, 1, 0),
			ret(unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)), ret(unix.SECCOMP_RET_ALLOW),
		)
	} else {
		f = append(f,
			jeq(uint32(unix.SYS_SOCKET), 1, 0), jeq(uint32(unix.SYS_SOCKETPAIR), 0, 9),
			ld(16), jeq(unix.AF_UNIX, 7, 0), jeq(unix.AF_NETLINK, 6, 0), jeq(unix.AF_INET, 2, 0), jeq(unix.AF_INET6, 1, 0),
			ret(unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)),
			ld(24), unix.SockFilter{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: 15}, jeq(unix.SOCK_RAW, 1, 0),
			ret(unix.SECCOMP_RET_ALLOW), ret(unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)),
		)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	p := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	r, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&p)))
	runtime.KeepAlive(f)
	if e != 0 {
		return e
	}
	if r != 0 {
		return fmt.Errorf("seccomp thread synchronization failed")
	}
	return nil
}

func launch(bin string, args []string) error {
	runtime.LockOSThread()
	if err := Install(); err != nil {
		return fmt.Errorf("[proxy_sandbox_unavailable] seccomp unavailable in this kernel or runtime: %w", err)
	}
	// Probe the exact operation required by routing_mark, including on old kernels.
	if bin == "/opt/ctlvps/bin/sing-box" {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_MARK, 0x43000001)
		unix.Close(fd)
		if err != nil {
			return fmt.Errorf("[proxy_mark_unavailable] SO_MARK with CAP_NET_RAW unavailable; check host kernel and container capabilities: %w", err)
		}
	}
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/nonexistent", "GOMEMLIMIT=" + os.Getenv("GOMEMLIMIT")}
	if bin == "/opt/ctlvps/bin/mita" {
		env = append(env, "MITA_CONFIG_JSON_FILE="+os.Getenv("MITA_CONFIG_JSON_FILE"), "MITA_UDS_PATH=/tmp/ctlvps-mita.sock", "MITA_INSECURE_UDS=1")
	}
	return unix.Exec(bin, append([]string{bin}, args...), env)
}
