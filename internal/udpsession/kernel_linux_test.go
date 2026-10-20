//go:build linux

package udpsession

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func attr(kind uint16, b []byte) []byte {
	out := make([]byte, (len(b)+7)&^3)
	binary.NativeEndian.PutUint16(out, uint16(len(b)+4))
	binary.NativeEndian.PutUint16(out[2:], kind)
	copy(out[4:], b)
	return out
}
func TestConntrackPreservesNATListenerAndIPv6ClientIdentity(t *testing.T) {
	for _, ip := range []string{"192.0.2.2", "2001:db8::2"} {
		addr := netip.MustParseAddr(ip)
		kind := uint16(1)
		if addr.Is6() {
			kind = 3
		}
		p := []byte{0xac, 0x00}
		listen := []byte{0x61, 0xa9}
		orig := append(attr(1, attr(kind, addr.AsSlice())), attr(2, append(attr(1, []byte{17}), attr(2, p)...))...)
		reply := attr(2, attr(2, listen))
		b := append([]byte{2, 0, 0, 0}, attr(1, orig)...)
		b = append(b, attr(2, reply)...)
		b = append(b, attr(8, []byte{0x45, 0, 0, 7})...)
		flow, ok, err := parseFlow(b)
		if err != nil || !ok || flow.ForwardID != 7 || flow.Source != netip.AddrPortFrom(addr, 44032) || flow.ListenPort != 25001 {
			t.Fatal(flow, ok, err)
		}
		b[len(b)-4] = 0x43
		if _, ok, err = parseFlow(b); err != nil || ok {
			t.Fatal("node conntrack consumed", ok, err)
		}
	}
	for _, b := range [][]byte{{1}, {4, 0, 1}, {3, 0, 1, 0}, {8, 0, 1, 0}} {
		if _, err := attributes(b); err == nil {
			t.Fatal("truncated attributes accepted")
		}
	}
}

func TestClosedUpstreamConntrackDoesNotConsumeClientAdmissionBudget(t *testing.T) {
	observed := map[Flow]bool{}
	for i := 0; i < MaxSessions; i++ {
		observed[Flow{1, netip.AddrPortFrom(netip.MustParseAddr("192.0.2.2"), uint16(10000+i)), 25001}] = true
	}
	out := map[Flow]bool{}
	// More stale outbound entries than the entire client budget, including
	// same-port targets, cannot consume observation capacity.
	for i := 0; i < MaxSessions*3; i++ {
		old := Flow{1, netip.AddrPortFrom(netip.MustParseAddr("198.51.100.1"), uint16(10000+i)), 25001}
		observeFlow(out, old, observed, true)
	}
	for f := range observed {
		observeFlow(out, f, observed, true)
	}
	if len(out) != MaxSessions {
		t.Fatal("outbound remnants consumed client capacity", len(out))
	}
	empty := map[Flow]bool{}
	for f := range observed {
		observeFlow(empty, f, nil, true)
	}
	if len(empty) != 0 {
		t.Fatal("empty snapshot granted unknown admissions")
	}
}
