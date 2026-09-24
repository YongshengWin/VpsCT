//go:build linux

package netinventory

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"testing"
)

func TestAddressUsability(t *testing.T) {
	base := make([]byte, 8)
	base[0], base[1] = unix.AF_INET6, 64
	native.PutUint32(base[4:], 7)
	base = append(base, attribute(unix.IFA_ADDRESS, net.ParseIP("2001:db8::1").To16())...)
	for _, flag := range []uint32{0, unix.IFA_F_TENTATIVE, unix.IFA_F_DADFAILED, unix.IFA_F_DEPRECATED, unix.IFA_F_OPTIMISTIC} {
		for _, extended := range []bool{false, true} {
			b := append([]byte(nil), base...)
			if extended {
				flags := make([]byte, 4)
				native.PutUint32(flags, flag)
				b = append(b, attribute(unix.IFA_FLAGS, flags)...)
			} else {
				b[2] = byte(flag)
			}
			index, address, usable, err := parseAddress(b)
			if err != nil || index != 7 || address != "2001:db8::1/64" || usable != (flag == 0) {
				t.Fatalf("flag=%x extended=%v: %d %s %v %v", flag, extended, index, address, usable, err)
			}
		}
	}
	lifetime := make([]byte, 16)
	native.PutUint32(lifetime[4:], 300)
	if _, _, usable, err := parseAddress(append(base, attribute(unix.IFA_CACHEINFO, lifetime)...)); err != nil || usable {
		t.Fatalf("expired preferred lifetime: %v %v", usable, err)
	}
	if _, _, _, err := parseAddress(append(base, attribute(unix.IFA_FLAGS, []byte{0})...)); err == nil {
		t.Fatal("truncated flags accepted")
	}
}

func attribute(kind uint16, data []byte) []byte {
	b := make([]byte, (len(data)+7)&^3)
	native.PutUint16(b, uint16(len(data)+4))
	native.PutUint16(b[2:], kind)
	copy(b[4:], data)
	return b
}

func TestParseLinkAndRoute(t *testing.T) {
	b := make([]byte, 16)
	native.PutUint32(b[4:], 7)
	native.PutUint32(b[8:], unix.IFF_UP|unix.IFF_LOWER_UP)
	b = append(b, attribute(unix.IFLA_IFNAME, []byte("custom0\x00"))...)
	b = append(b, attribute(unix.IFLA_LINKINFO, attribute(unix.IFLA_INFO_KIND, []byte("wireguard\x00")))...)
	stats := make([]byte, 32)
	native.PutUint64(stats[16:], 123)
	native.PutUint64(stats[24:], 456)
	b = append(b, attribute(unix.IFLA_STATS64, stats)...)
	l, err := parseLink(b)
	if err != nil || l.Kind != "wireguard" || l.Name != "custom0" || !l.CountersValid || l.Rx != 123 || l.Tx != 456 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := parseLink(b[:17]); err == nil {
		t.Fatal("accepted truncated attribute")
	}
	route := make([]byte, 12)
	route[0], route[4], route[7] = unix.AF_INET6, unix.RT_TABLE_MAIN, unix.RTN_UNICAST
	index := make([]byte, 4)
	native.PutUint32(index, 7)
	route = append(route, attribute(unix.RTA_OIF, index)...)
	family, indexes, err := defaultRoute(route)
	if err != nil || family != unix.AF_INET6 || len(indexes) != 1 || indexes[0] != 7 {
		t.Fatalf("route: %v %v %v", family, indexes, err)
	}
	route[4] = 100
	if _, indexes, err = defaultRoute(route); err != nil || len(indexes) != 0 {
		t.Fatal("policy table misreported as main default")
	}
}

func TestLinuxInventory(t *testing.T) {
	c := New(t.TempDir(), "container-test-boot")
	defer c.Close()
	s := c.Collect()
	if s.Status != "ok" && s.Status != "incomplete" {
		t.Fatalf("collection failed: %s", s.Error)
	}
	found := false
	for _, l := range s.Interfaces {
		if l.Kind == "loopback" {
			found = true
		}
		if l.ID == "" || l.Generation == "" {
			t.Fatal("missing identity")
		}
	}
	if !found {
		t.Fatal("loopback not observed")
	}
}

// Run only inside an explicitly isolated network namespace (see the script).
func TestNetworkNamespaceLifecycle(t *testing.T) {
	if os.Getenv("CTLVPS_NETWORK_NAMESPACE_TEST") != "1" {
		t.Skip("requires isolated network namespace")
	}
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %s %v", args, out, err)
		}
	}
	ip("link", "add", "dev", "np-test0", "index", "501", "address", "02:00:00:00:00:01", "type", "dummy")
	defer exec.Command("ip", "link", "del", "dev", "np-test0").Run()
	ip("link", "set", "dev", "np-test0", "up")
	ip("-6", "addr", "add", "2001:db8::1/64", "dev", "np-test0", "nodad")
	ip("-6", "route", "add", "default", "dev", "np-test0")
	c := New(t.TempDir(), "namespace-boot")
	defer c.Close()
	find := func() link {
		t.Helper()
		s := c.Collect()
		if s.Status != "ok" {
			t.Fatalf("snapshot %s %s", s.Status, s.Error)
		}
		for _, n := range s.Interfaces {
			if n.Index == 501 {
				return link{NetworkInterface: n}
			}
		}
		t.Fatal("missing interface")
		return link{}
	}
	first := find()
	if first.Kind != "dummy" || !first.DefaultIPv6 || first.DefaultIPv4 || len(first.Addresses) == 0 {
		t.Fatalf("IPv6-only interface: %+v", first)
	}
	ip("link", "set", "dev", "np-test0", "name", "np-renamed")
	renamed := find()
	if renamed.ID != first.ID || renamed.Name != "np-renamed" {
		t.Fatal("rename changed identity")
	}
	ip("link", "del", "dev", "np-renamed")
	ip("link", "add", "dev", "np-test0", "index", "501", "address", "02:00:00:00:00:01", "type", "dummy")
	replaced := find()
	if replaced.ID == first.ID {
		t.Fatal("reused ifindex resurrected old interface")
	}
}
