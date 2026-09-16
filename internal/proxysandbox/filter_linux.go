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
		// x32 is not a supported ABI even though it uses AUDIT_ARCH_X86_64.
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jt: 0, Jf: 1}, ret(unix.SECCOMP_RET_KILL_PROCESS),
		jeq(uint32(unix.SYS_SOCKET), 0, 7),
		ld(16), jeq(unix.AF_PACKET, 6, 0), jeq(unix.AF_INET, 1, 0), jeq(unix.AF_INET6, 0, 3),
		ld(24), {Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: 15}, jeq(unix.SOCK_RAW, 1, 0),
		ret(unix.SECCOMP_RET_ALLOW), ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)),
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
		return err
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
			return fmt.Errorf("SO_MARK without NET_ADMIN unavailable: %w", err)
		}
	}
	return unix.Exec(bin, append([]string{bin}, args...), []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/nonexistent", "GOMEMLIMIT=" + os.Getenv("GOMEMLIMIT")})
}
