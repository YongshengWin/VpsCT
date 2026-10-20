//go:build linux

package udpsession

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func attributes(b []byte) (map[uint16][]byte, error) {
	out := map[uint16][]byte{}
	for len(b) > 0 {
		if len(b) < 4 {
			return nil, errors.New("short conntrack attribute")
		}
		n := int(binary.NativeEndian.Uint16(b))
		kind := binary.NativeEndian.Uint16(b[2:]) & 0x3fff
		aligned := (n + 3) &^ 3
		if n < 4 || aligned > len(b) {
			return nil, errors.New("invalid conntrack attribute")
		}
		if _, ok := out[kind]; ok {
			return nil, errors.New("duplicate conntrack attribute")
		}
		out[kind] = b[4:n]
		b = b[aligned:]
	}
	return out, nil
}

func parseFlow(b []byte) (Flow, bool, error) {
	if len(b) < 4 {
		return Flow{}, false, errors.New("short conntrack entry")
	}
	a, err := attributes(b[4:])
	if err != nil {
		return Flow{}, false, err
	}
	if len(a[8]) != 4 {
		return Flow{}, false, nil
	}
	mark := binary.BigEndian.Uint32(a[8])
	if mark&0xff000000 != 0x45000000 {
		return Flow{}, false, nil
	}
	orig, err := attributes(a[1])
	if err != nil {
		return Flow{}, false, err
	}
	reply, err := attributes(a[2])
	if err != nil {
		return Flow{}, false, err
	}
	proto, err := attributes(orig[2])
	if err != nil {
		return Flow{}, false, err
	}
	rproto, err := attributes(reply[2])
	if err != nil {
		return Flow{}, false, err
	}
	if len(proto[1]) != 1 || proto[1][0] != 17 {
		return Flow{}, false, nil
	}
	ip, err := attributes(orig[1])
	if err != nil {
		return Flow{}, false, err
	}
	src := ip[1]
	if len(src) == 0 {
		src = ip[3]
	}
	addr, ok := netip.AddrFromSlice(src)
	if !ok || len(proto[2]) != 2 || len(rproto[2]) != 2 {
		return Flow{}, false, errors.New("invalid UDP conntrack tuple")
	}
	return Flow{int64(mark & 0xffffff), netip.AddrPortFrom(addr.Unmap(), binary.BigEndian.Uint16(proto[2])), binary.BigEndian.Uint16(rproto[2])}, true, nil
}

// Read only the forward-marked kernel conntrack entries. No conntrack entry or
// timeout is modified here. nft remains the sole admission/timeout authority.
func Live(ctx context.Context) (map[Flow]bool, error) { return live(ctx, nil, false) }

// Closed upstream sockets leave conntrack entries behind. They are not client
// admissions and must not consume the bounded client-observation map.
func LiveFor(ctx context.Context, observed map[Flow]bool) (map[Flow]bool, error) {
	if len(observed) > MaxSessions {
		return nil, errors.New("client observation exceeds budget")
	}
	return live(ctx, observed, true)
}

func observeFlow(out map[Flow]bool, flow Flow, observed map[Flow]bool, scoped bool) {
	if !scoped || observed[flow] {
		out[flow] = true
	}
}

func live(ctx context.Context, observed map[Flow]bool, scoped bool) (map[Flow]bool, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_NETFILTER)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	req := make([]byte, 36)
	binary.NativeEndian.PutUint32(req, uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:], 0x101) // CTNETLINK / CT_GET
	binary.NativeEndian.PutUint16(req[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	binary.NativeEndian.PutUint32(req[8:], 1)
	for i, v := range []struct {
		kind  uint16
		value uint32
	}{{8, 0x45000000}, {21, 0xff000000}} {
		b := req[20+i*8:]
		binary.NativeEndian.PutUint16(b, 8)
		binary.NativeEndian.PutUint16(b[2:], v.kind)
		binary.BigEndian.PutUint32(b[4:], v.value)
	}
	if err = unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	out := map[Flow]bool{}
	buf := make([]byte, 64<<10)
	total := 0
	deadline := time.Now().Add(time.Second)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, errors.New("conntrack observation timed out")
		}
		n, _, flags, from, e := unix.Recvmsg(fd, buf, nil, 0)
		if e == unix.EAGAIN || e == unix.EINTR {
			_, e = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 20)
			if e != nil && e != unix.EINTR {
				return nil, e
			}
			continue
		}
		if e != nil {
			return nil, e
		}
		sender, ok := from.(*unix.SockaddrNetlink)
		if !ok || sender.Pid != 0 || flags&unix.MSG_TRUNC != 0 {
			return nil, errors.New("invalid conntrack sender or truncated dump")
		}
		total += n
		if total > 16<<20 {
			return nil, errors.New("conntrack observation exceeds byte budget")
		}
		messages, e := syscall.ParseNetlinkMessage(buf[:n])
		if e != nil {
			return nil, e
		}
		for _, m := range messages {
			if m.Header.Seq != 1 || m.Header.Flags&unix.NLM_F_DUMP_INTR != 0 {
				return nil, errors.New("interrupted conntrack snapshot")
			}
			if m.Header.Type == unix.NLMSG_DONE {
				if len(m.Data) >= 4 && binary.NativeEndian.Uint32(m.Data) != 0 {
					return nil, errors.New("incomplete conntrack snapshot")
				}
				return out, nil
			}
			if m.Header.Type == unix.NLMSG_ERROR {
				return nil, errors.New("conntrack snapshot rejected")
			}
			if m.Header.Type != 0x100 {
				return nil, errors.New("unexpected conntrack response")
			}
			flow, ok, e := parseFlow(m.Data)
			if e != nil {
				return nil, e
			}
			if ok {
				observeFlow(out, flow, observed, scoped)
				if len(out) > MaxSessions*2 {
					return nil, errors.New("conntrack session observation exceeds budget")
				}
			}
		}
	}
}

// Discover the ephemeral loopback listener through this exact core process's
// socket descriptors. Exclude every configured proxy/forward listener.
func Discover(pid int, excluded map[int]bool) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid core process")
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return "", err
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		link, e := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if e == nil && strings.HasPrefix(link, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/tcp", pid))
	if err != nil {
		return "", err
	}
	var address string
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != "0A" || !inodes[f[9]] {
			continue
		}
		host, port, ok := strings.Cut(f[1], ":")
		if !ok || host != "0100007F" {
			continue
		}
		p, e := strconv.ParseUint(port, 16, 16)
		if e != nil || p == 0 || excluded[int(p)] {
			continue
		}
		if address != "" {
			return "", errors.New("ambiguous local connection manager listener")
		}
		address = "127.0.0.1:" + strconv.Itoa(int(p))
	}
	if address == "" {
		return "", errors.New("local connection manager listener unavailable")
	}
	return address, nil
}
