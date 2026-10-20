//go:build linux

package main

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// A real loopback SOCKS UDP associate isolates response timing from the
// production proxy, routing and guard. The origin returns exactly one packet.
func delayedAssociate(t *testing.T, delay time.Duration, reply bool) int {
	t.Helper()
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		control.Close()
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		conn, err := control.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		greeting := make([]byte, 3)
		if _, err = io.ReadFull(conn, greeting); err != nil {
			return
		}
		if _, err = conn.Write([]byte{5, 0}); err != nil {
			return
		}
		request := make([]byte, 10)
		if _, err = io.ReadFull(conn, request); err != nil {
			return
		}
		port := relay.LocalAddr().(*net.UDPAddr).Port
		if _, err = conn.Write(append([]byte{5, 0, 0}, address("127.0.0.1", port)...)); err != nil {
			return
		}
		<-stop
	}()
	go func() {
		defer workers.Done()
		packet := make([]byte, 2048)
		n, peer, err := relay.ReadFrom(packet)
		if err != nil {
			return
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		if reply {
			_, _ = relay.WriteTo(packet[:n], peer)
		}
	}()
	t.Cleanup(func() { close(stop); control.Close(); relay.Close(); workers.Wait() })
	return control.Addr().(*net.TCPAddr).Port
}

func TestUDPProbeNativeSchedulingBudget(t *testing.T) {
	port := delayedAssociate(t, 900*time.Millisecond, true)
	started := time.Now()
	got, err := udpAtWithin(port, "2001:db8:ffff::10", 2*time.Second)
	if err != nil || got != "fixture" {
		t.Fatalf("delayed IPv6 reply: got %q, err %v", got, err)
	}
	if elapsed := time.Since(started); elapsed < 700*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("unexpected response time %v", elapsed)
	}
}

func TestUDPProbeStillRejectsSilence(t *testing.T) {
	port := delayedAssociate(t, 0, false)
	started := time.Now()
	_, err := udpAtWithin(port, "203.0.113.10", 80*time.Millisecond)
	timeout, ok := err.(net.Error)
	if !ok || !timeout.Timeout() {
		t.Fatalf("expected bounded timeout, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("silent response exceeded bound")
	}
}

func TestUDPRawProbeKeepsShortBudget(t *testing.T) {
	port := delayedAssociate(t, 900*time.Millisecond, true)
	_, err := udpAt(port, "203.0.113.10")
	timeout, ok := err.(net.Error)
	if !ok || !timeout.Timeout() {
		t.Fatalf("raw fixture lost its short deadline: %v", err)
	}
}
