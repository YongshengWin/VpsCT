//go:build linux

package proxysandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Seccomp is irreversible for the process and all its threads. Keep these
// genuine syscall probes inside a short-lived child, never the test runner.
func TestKernelSocketSandbox(t *testing.T) {
	for _, mode := range []string{"proxy", "guard"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKernelSocketSandboxHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "CTLVPS_SANDBOX_TEST_CHILD="+mode)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("kernel %s sandbox failed: %v\n%s", mode, err, out)
			}
		})
	}
}

func TestKernelSocketSandboxHelper(t *testing.T) {
	mode := os.Getenv("CTLVPS_SANDBOX_TEST_CHILD")
	if mode == "" {
		t.Skip("isolated seccomp helper")
	}
	if mode != "proxy" && mode != "guard" {
		t.Fatal("invalid isolated sandbox mode")
	}
	blockedFamily := unix.AF_VSOCK
	if mode == "guard" {
		blockedFamily = unix.AF_INET
	}
	// Both threads exist before installation: TSYNC must apply the same family
	// restriction to them, instead of leaving a pre-existing thread unfiltered.
	ready := make(chan struct{}, 2)
	probe := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready <- struct{}{}
			<-probe
			fd, err := unix.Socket(blockedFamily, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
			if err == nil {
				unix.Close(fd)
			}
			results <- err
		}()
	}
	<-ready
	<-ready
	var err error
	if mode == "guard" {
		err = InstallGuard()
	} else {
		err = Install()
	}
	if err != nil {
		t.Fatal("seccomp unavailable:", err)
	}
	close(probe)
	for range 2 {
		if err := <-results; !errors.Is(err, unix.EPERM) {
			t.Fatal("a pre-existing thread bypassed the socket filter:", err)
		}
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), "NoNewPrivs:\t1\n") || !strings.Contains(string(status), "Seccomp:\t2\n") {
		t.Fatal("kernel did not activate NoNewPrivileges and seccomp")
	}
	for _, spec := range []struct{ family, kind, protocol int }{
		{unix.AF_UNIX, unix.SOCK_STREAM, 0},
		{unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE},
	} {
		fd, err := unix.Socket(spec.family, spec.kind|unix.SOCK_CLOEXEC, spec.protocol)
		if err != nil {
			t.Fatalf("required control socket family %d denied: %v", spec.family, err)
		}
		unix.Close(fd)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal("required Unix socketpair denied:", err)
	}
	unix.Close(pair[0])
	unix.Close(pair[1])
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		fd, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			unix.Close(fd)
		}
		if mode == "guard" {
			if !errors.Is(err, unix.EPERM) {
				t.Fatalf("guard Internet socket family %d escaped: %v", family, err)
			}
			pair, err := unix.Socketpair(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
			if err == nil {
				unix.Close(pair[0])
				unix.Close(pair[1])
			}
			if !errors.Is(err, unix.EPERM) {
				t.Fatalf("guard Internet socketpair family %d escaped: %v", family, err)
			}
		} else if err != nil && !(family == unix.AF_INET6 && errors.Is(err, unix.EAFNOSUPPORT)) {
			t.Fatalf("proxy data socket family %d denied: %v", family, err)
		}
	}
	for _, family := range []int{unix.AF_UNSPEC, unix.AF_PACKET, unix.AF_VSOCK, unix.AF_ALG, unix.AF_TIPC} {
		fd, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			unix.Close(fd)
		}
		if !errors.Is(err, unix.EPERM) {
			t.Fatalf("unsupported socket family %d was not filtered: %v", family, err)
		}
		pair, err := unix.Socketpair(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			unix.Close(pair[0])
			unix.Close(pair[1])
		}
		if !errors.Is(err, unix.EPERM) {
			t.Fatalf("unsupported socketpair family %d was not filtered: %v", family, err)
		}
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.IPPROTO_ICMP)
	if err == nil {
		unix.Close(fd)
	}
	if !errors.Is(err, unix.EPERM) {
		t.Fatal("raw Internet socket was not filtered:", err)
	}
	if err := unix.Unshare(unix.CLONE_NEWUSER); !errors.Is(err, unix.EPERM) {
		t.Fatal("namespace creation was not filtered:", err)
	}
	_, _, callErr := unix.RawSyscall(unix.SYS_IO_URING_SETUP, 1, 0, 0)
	if callErr != unix.EPERM {
		t.Fatal("io_uring bypass was not filtered:", callErr)
	}
}
