//go:build linux

package netinventory

import (
	"context"
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// Watch opens its own read-only subscription before returning. It does not
// consume Collector's deletion stream or write its identity registry. On fatal
// failure it publishes Lost and closes; callers must stop renewing leases.
func Watch(ctx context.Context) (<-chan Change, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	out := make(chan Change, 1)
	go func() {
		defer close(out)
		defer unix.Close(fd)
		buf := make([]byte, 64<<10)
		for ctx.Err() == nil {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, err := unix.Poll(poll, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				publishChange(out, Change{Lost: true})
				return
			}
			if poll[0].Revents&unix.POLLIN == 0 {
				continue
			}
			change := Change{Indices: map[int]bool{}}
			budget := 0
			for budget < 1<<20 {
				n, _, flags, from, err := unix.Recvmsg(fd, buf, nil, unix.MSG_DONTWAIT)
				if errors.Is(err, unix.EAGAIN) {
					break
				}
				if errors.Is(err, unix.ENOBUFS) {
					publishChange(out, Change{Lost: true})
					return
				}
				if err != nil || n == 0 {
					publishChange(out, Change{Lost: true})
					return
				}
				budget += n
				peer, ok := from.(*unix.SockaddrNetlink)
				if !ok || peer.Pid != 0 {
					continue
				}
				if flags&unix.MSG_TRUNC != 0 {
					change.Lost = true
					continue
				}
				messages, err := syscall.ParseNetlinkMessage(buf[:n])
				if err != nil {
					change.Lost = true
					continue
				}
				for _, m := range messages {
					switch m.Header.Type {
					case unix.RTM_NEWLINK, unix.RTM_DELLINK:
						if len(m.Data) < 16 {
							change.Lost = true
						} else {
							change.Indices[u32(m.Data[4:])] = true
						}
					case unix.RTM_NEWADDR, unix.RTM_DELADDR:
						if len(m.Data) < 8 {
							change.Lost = true
						} else {
							change.Indices[u32(m.Data[4:])] = true
						}
					case unix.NLMSG_OVERRUN, unix.NLMSG_ERROR:
						change.Lost = true
					}
				}
				if len(change.Indices) > 256 {
					change.Lost, change.Indices = true, map[int]bool{}
				}
			}
			if budget >= 1<<20 {
				publishChange(out, Change{Lost: true})
				return
			}
			if change.Lost || len(change.Indices) > 0 {
				publishChange(out, change)
			}
		}
	}()
	return out, nil
}
