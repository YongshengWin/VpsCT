//go:build linux

package netinventory

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"ctlvps/internal/agentproto"
	"golang.org/x/sys/unix"
)

var native = binary.NativeEndian

// Link notifications detect deletion/recreation between polling samples, even
// when the kernel reuses an ifindex. Overflow loses continuity conservatively.
func newSource() (func() (reading, error), func() error) {
	fd := -1
	read := func() (reading, error) {
		reset := fd < 0
		if fd < 0 {
			var err error
			fd, err = unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
			if err != nil {
				return reading{}, err
			}
			if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: unix.RTMGRP_LINK}); err != nil {
				unix.Close(fd)
				fd = -1
				return reading{}, err
			}
		}
		deleted, lost, err := drainLinks(fd)
		if err != nil {
			unix.Close(fd)
			fd = -1
			return reading{}, err
		}
		r, err := readNetwork()
		r.Deleted, r.Reset = deleted, reset || lost
		if err != nil {
			return r, err
		}
		late, lateLost, err := drainLinks(fd)
		if err != nil {
			unix.Close(fd)
			fd = -1
			return reading{Reset: true, Deleted: deleted}, err
		}
		for index := range late {
			deleted[index] = true
		}
		// A delete concurrent with the dump can make its counters stale; omit it.
		if len(late) > 0 {
			kept := r.Links[:0]
			for _, l := range r.Links {
				if !late[l.Index] {
					kept = append(kept, l)
				}
			}
			r.Links, r.Complete = kept, false
		}
		r.Deleted, r.Reset = deleted, reset || lost || lateLost
		return r, nil
	}
	close := func() error {
		if fd < 0 {
			return nil
		}
		err := unix.Close(fd)
		fd = -1
		return err
	}
	return read, close
}

func drainLinks(fd int) (map[int]bool, bool, error) {
	deleted := map[int]bool{}
	lost := false
	buf := make([]byte, 64<<10)
	for budget := 0; budget < 1<<20; {
		n, _, flags, from, err := unix.Recvmsg(fd, buf, nil, unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EAGAIN) {
			return deleted, lost, nil
		}
		if errors.Is(err, unix.ENOBUFS) {
			lost = true
			continue
		}
		if err != nil {
			return nil, true, err
		}
		if n == 0 {
			return nil, true, errors.New("closed link monitor")
		}
		budget += n
		if peer, ok := from.(*unix.SockaddrNetlink); !ok || peer.Pid != 0 {
			continue
		}
		if flags&unix.MSG_TRUNC != 0 {
			lost = true
			continue
		}
		messages, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			lost = true
			continue
		}
		for _, msg := range messages {
			if msg.Header.Type == unix.NLMSG_OVERRUN {
				lost = true
			}
			if msg.Header.Type == unix.RTM_DELLINK && len(msg.Data) >= 16 {
				deleted[u32(msg.Data[4:])] = true
			}
		}
	}
	return nil, true, errors.New("link monitor exceeds budget")
}

// dump uses a private unicast socket with bounded time and memory. An interrupted
// dump is rejected rather than interpreted as interfaces disappearing.
func dump(kind uint16, bodySize int) ([]syscall.NetlinkMessage, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	if err = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		return nil, err
	}
	request := make([]byte, unix.NLMSG_HDRLEN+bodySize)
	native.PutUint32(request, uint32(len(request)))
	native.PutUint16(request[4:], kind)
	native.PutUint16(request[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	native.PutUint32(request[8:], 1)
	if err = unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	buf := make([]byte, 64<<10)
	var result []syscall.NetlinkMessage
	for budget := 0; budget < 4<<20; {
		n, _, flags, from, err := unix.Recvmsg(fd, buf, nil, 0)
		if err != nil {
			return nil, err
		}
		peer, ok := from.(*unix.SockaddrNetlink)
		if !ok || peer.Pid != 0 || n == 0 || flags&unix.MSG_TRUNC != 0 {
			return nil, errors.New("invalid netlink datagram")
		}
		budget += n
		data := append([]byte(nil), buf[:n]...)
		messages, err := syscall.ParseNetlinkMessage(data)
		if err != nil {
			return nil, err
		}
		for _, msg := range messages {
			if msg.Header.Seq != 1 || msg.Header.Flags&unix.NLM_F_DUMP_INTR != 0 {
				return nil, errors.New("interrupted netlink dump")
			}
			switch msg.Header.Type {
			case unix.NLMSG_DONE:
				if len(msg.Data) >= 4 && native.Uint32(msg.Data) != 0 {
					return nil, errors.New("failed netlink dump")
				}
				return result, nil
			case unix.NLMSG_ERROR, unix.NLMSG_OVERRUN:
				return nil, errors.New("failed netlink dump")
			default:
				result = append(result, msg)
			}
		}
	}
	return nil, errors.New("netlink dump exceeds budget")
}

func attrs(data []byte) (map[uint16][]byte, error) {
	result := map[uint16][]byte{}
	for len(data) != 0 {
		if len(data) < 4 {
			return nil, errors.New("short netlink attribute")
		}
		n := int(native.Uint16(data))
		if n < 4 || n > len(data) {
			return nil, errors.New("invalid netlink attribute")
		}
		result[native.Uint16(data[2:])&0x3fff] = data[4:n]
		aligned := (n + 3) & ^3
		if aligned > len(data) {
			if n == len(data) {
				break
			}
			return nil, errors.New("invalid attribute padding")
		}
		data = data[aligned:]
	}
	return result, nil
}

func u32(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(native.Uint32(b))
}
func cstring(b []byte) string { return strings.TrimRight(string(b), "\x00") }

func parseLink(data []byte) (link, error) {
	var l link
	if len(data) < 16 {
		return l, errors.New("short link")
	}
	a, err := attrs(data[16:])
	if err != nil {
		return l, err
	}
	l.Index, l.Name = u32(data[4:]), cstring(a[unix.IFLA_IFNAME])
	l.MTU, l.ParentIndex, l.MasterIndex = u32(a[unix.IFLA_MTU]), u32(a[unix.IFLA_LINK]), u32(a[unix.IFLA_MASTER])
	flags := u32(data[8:])
	l.Up, l.Carrier = flags&unix.IFF_UP != 0, flags&unix.IFF_LOWER_UP != 0
	l.Kind = "unknown"
	if flags&unix.IFF_LOOPBACK != 0 {
		l.Kind = "loopback"
	}
	if info := a[unix.IFLA_LINKINFO]; len(info) > 0 {
		i, err := attrs(info)
		if err != nil {
			return l, err
		}
		if kind := cstring(i[unix.IFLA_INFO_KIND]); kind != "" {
			l.Kind = kind
		}
	}
	if mac := a[unix.IFLA_ADDRESS]; len(mac) != 0 {
		l.MAC = net.HardwareAddr(mac).String()
	}
	l.Addresses = []string{}
	if b := a[unix.IFLA_STATS64]; len(b) >= 32 {
		rx, tx := native.Uint64(b[16:]), native.Uint64(b[24:])
		if rx <= math.MaxInt64/4 && tx <= math.MaxInt64/4 {
			l.Rx, l.Tx, l.CountersValid = int64(rx), int64(tx), true
		}
	}
	return l, nil
}

func parseAddress(data []byte) (int, string, bool, error) {
	if len(data) < 8 {
		return 0, "", false, errors.New("short address")
	}
	if data[0] != unix.AF_INET && data[0] != unix.AF_INET6 {
		return 0, "", false, nil
	}
	a, err := attrs(data[8:])
	if err != nil {
		return 0, "", false, err
	}
	b := a[unix.IFA_LOCAL]
	if len(b) == 0 {
		b = a[unix.IFA_ADDRESS]
	}
	ip, ok := netip.AddrFromSlice(b)
	if !ok || (data[0] == unix.AF_INET && !ip.Is4()) || (data[0] == unix.AF_INET6 && !ip.Is6()) {
		return 0, "", false, errors.New("invalid address")
	}
	prefix := netip.PrefixFrom(ip, int(data[1]))
	if !prefix.IsValid() {
		return 0, "", false, errors.New("invalid prefix")
	}
	flags := uint32(data[2])
	if raw, ok := a[unix.IFA_FLAGS]; ok {
		if len(raw) != 4 {
			return 0, "", false, errors.New("invalid address flags")
		}
		flags = native.Uint32(raw)
	}
	usable := flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED|unix.IFA_F_DEPRECATED|unix.IFA_F_OPTIMISTIC) == 0
	if raw, ok := a[unix.IFA_CACHEINFO]; ok {
		if len(raw) < 16 {
			return 0, "", false, errors.New("invalid address lifetime")
		}
		usable = usable && native.Uint32(raw) != 0 && native.Uint32(raw[4:]) != 0
	}
	return u32(data[4:]), prefix.String(), usable, nil
}

func defaultRoute(data []byte) (byte, []int, error) {
	if len(data) < 12 {
		return 0, nil, errors.New("short route")
	}
	if (data[0] != unix.AF_INET && data[0] != unix.AF_INET6) || data[1] != 0 || data[2] != 0 || data[7] != unix.RTN_UNICAST {
		return 0, nil, nil
	}
	a, err := attrs(data[12:])
	if err != nil {
		return 0, nil, err
	}
	table := int(data[4])
	if len(a[unix.RTA_TABLE]) >= 4 {
		table = u32(a[unix.RTA_TABLE])
	}
	if table != unix.RT_TABLE_MAIN {
		return 0, nil, nil
	}
	var indexes []int
	if index := u32(a[unix.RTA_OIF]); index > 0 {
		indexes = append(indexes, index)
	}
	for b := a[unix.RTA_MULTIPATH]; len(b) > 0; {
		if len(b) < 8 {
			return 0, nil, errors.New("short next hop")
		}
		n := int(native.Uint16(b))
		if n < 8 || n > len(b) {
			return 0, nil, errors.New("invalid next hop")
		}
		if b[2]&unix.RTNH_F_DEAD == 0 {
			indexes = append(indexes, u32(b[4:]))
		}
		n = (n + 3) & ^3
		if n > len(b) {
			return 0, nil, errors.New("invalid next hop padding")
		}
		b = b[n:]
	}
	return data[0], indexes, nil
}

func readNetwork() (reading, error) {
	r := reading{Complete: true}
	messages, err := dump(unix.RTM_GETLINK, 16)
	if err != nil {
		return r, err
	}
	for _, m := range messages {
		if m.Header.Type != unix.RTM_NEWLINK {
			continue
		}
		l, err := parseLink(m.Data)
		if err != nil {
			return r, err
		}
		if l.Name == "" || strings.ContainsAny(l.Name, "/:\x00") {
			return r, errors.New("invalid interface name")
		}
		if device, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", l.Name, "device")); err == nil {
			l.Device = device
			if l.Kind == "unknown" {
				l.Kind = "physical"
			}
		}
		if !l.CountersValid {
			r.Complete = false
		}
		r.Links = append(r.Links, l)
	}
	sort.Slice(r.Links, func(i, j int) bool { return r.Links[i].Index < r.Links[j].Index })
	byIndex := map[int]*link{}
	for i := range r.Links {
		byIndex[r.Links[i].Index] = &r.Links[i]
	}
	addresses, err := dump(unix.RTM_GETADDR, 8)
	if err != nil {
		r.Complete = false
	} else {
		for _, m := range addresses {
			if m.Header.Type != unix.RTM_NEWADDR {
				continue
			}
			index, address, usable, err := parseAddress(m.Data)
			if err != nil {
				r.Complete = false
				continue
			}
			if l := byIndex[index]; l != nil && address != "" {
				if len(l.Addresses) < agentproto.MaxInterfaceAddresses {
					l.Addresses = append(l.Addresses, address)
					if usable {
						l.UsableAddresses = append(l.UsableAddresses, address)
					}
				} else {
					r.Complete = false
				}
			}
		}
	}
	routes, err := dump(unix.RTM_GETROUTE, 12)
	if err != nil {
		r.Complete = false
	} else {
		for _, m := range routes {
			if m.Header.Type != unix.RTM_NEWROUTE {
				continue
			}
			family, indexes, err := defaultRoute(m.Data)
			if err != nil {
				r.Complete = false
				continue
			}
			for _, index := range indexes {
				if l := byIndex[index]; l != nil {
					if family == unix.AF_INET {
						l.DefaultIPv4 = true
					} else if family == unix.AF_INET6 {
						l.DefaultIPv6 = true
					}
				}
			}
		}
	}
	return r, nil
}
