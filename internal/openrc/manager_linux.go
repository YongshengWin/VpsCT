//go:build linux

package openrc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const managerSocket = runtimeDir + "/manage.sock"

type controlReply struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func managedControl(ctx context.Context, args []string) (string, error) {
	st, e := os.Lstat(managerSocket)
	if e != nil {
		return "", e
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0600 {
		return "", errors.New("untrusted OpenRC service-host socket")
	}
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", managerSocket)
	if e != nil {
		return "", e
	}
	defer c.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()
	deadline := time.Now().Add(35 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	if e = json.NewEncoder(c).Encode(args); e != nil {
		return "", e
	}
	var reply controlReply
	if e = json.NewDecoder(io.LimitReader(c, (1<<20)+4096)).Decode(&reply); e != nil {
		return "", e
	}
	if reply.Error != "" {
		return "", errors.New(reply.Error)
	}
	return reply.Output, nil
}

func ensureManager(ctx context.Context) error {
	u := "ctlvps-service-host.service"
	if _, e := writeProtected(filepath.Join(UnitDir, u), []byte("# Managed by VpsCT (OpenRC v1)\n"), 0644); e != nil {
		return e
	}
	if _, e := installScript(u); e != nil {
		return e
	}
	if _, e := controlLocal(ctx, "enable", u); e != nil {
		return e
	}
	if _, e := controlLocal(ctx, "is-active", u); e != nil {
		if _, e = controlLocal(ctx, "start", u); e != nil {
			return e
		}
	}
	for i := 0; i < 50; i++ {
		if _, e := managedControl(ctx, []string{"is-active", u}); e == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("OpenRC service-host did not become ready")
}

// The guard keeps only NET_ADMIN. A separate root supervisor performs the
// fixed service operations, just as PID 1 does for the systemd backend.
func managerLoop() error {
	if os.Geteuid() != 0 || !Enabled() {
		return errors.New("OpenRC service-host requires root")
	}
	if e := trustedDirectory(runtimeDir, true); e != nil {
		return e
	}
	fd, e := unix.Open(runtimeDir+"/manager.lock", unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return e
	}
	if st, e := os.Lstat(managerSocket); e == nil {
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || st.Mode()&os.ModeSocket == 0 {
			return errors.New("unsafe stale service-host socket")
		}
		if e = os.Remove(managerSocket); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: managerSocket, Net: "unix"})
	if e != nil {
		return e
	}
	defer listener.Close()
	if e = os.Chmod(managerSocket, 0600); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() { <-ctx.Done(); listener.Close() }()
	slots := make(chan struct{}, 4)
	for {
		c, e := listener.AcceptUnix()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		go func() {
			defer func() { <-slots; c.Close() }()
			_ = c.SetDeadline(time.Now().Add(35 * time.Second))
			raw, e := c.SyscallConn()
			if e != nil {
				return
			}
			uid := uint32(1)
			if e = raw.Control(func(fd uintptr) {
				cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
				if err == nil {
					uid = cred.Uid
				}
			}); e != nil || uid != 0 {
				return
			}
			var args []string
			if e = json.NewDecoder(io.LimitReader(c, 4096)).Decode(&args); e != nil || len(args) > 32 {
				return
			}
			call, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out, e := controlLocal(call, args...)
			reply := controlReply{Output: out}
			if e != nil {
				reply.Error = fmt.Sprint(e)
			}
			_ = json.NewEncoder(c).Encode(reply)
		}()
	}
}
