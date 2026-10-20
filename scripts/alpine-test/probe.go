//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func echo() {
	echoAt([]string{"203.0.113.10:18080", "10.123.0.2:18080"})
}

func echoAt(addresses []string) {
	for _, address := range addresses {
		l, e := net.Listen("tcp", address)
		must(e)
		u, e := net.ListenPacket("udp", address)
		must(e)
		go func() {
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
			}
		}()
		go func() {
			buf := make([]byte, 2048)
			for {
				n, addr, e := u.ReadFrom(buf)
				must(e)
				_, e = u.WriteTo(buf[:n], addr)
				must(e)
			}
		}()
	}
	select {}
}

func socks(command byte, host string) (net.Conn, *net.UDPAddr, error) {
	c, e := net.DialTimeout("tcp", "127.0.0.1:1080", time.Second)
	if e != nil {
		return nil, nil, e
	}
	fail := func(e error) (net.Conn, *net.UDPAddr, error) { c.Close(); return nil, nil, e }
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, e = c.Write([]byte{5, 1, 0})
	if e != nil {
		return fail(e)
	}
	b := make([]byte, 2)
	_, e = io.ReadFull(c, b)
	if e != nil {
		return fail(e)
	}
	if !bytes.Equal(b, []byte{5, 0}) {
		return fail(fmt.Errorf("SOCKS auth denied"))
	}
	ip4 := net.ParseIP(host).To4()
	if ip4 == nil {
		return fail(fmt.Errorf("fixture target must be IPv4"))
	}
	req := append([]byte{5, command, 0, 1}, ip4...)
	req = append(req, 0x46, 0xa0)
	if command == 3 {
		req = []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}
	}
	_, e = c.Write(req)
	if e != nil {
		return fail(e)
	}
	head := make([]byte, 4)
	_, e = io.ReadFull(c, head)
	if e != nil {
		return fail(e)
	}
	if head[1] != 0 {
		return fail(fmt.Errorf("SOCKS request denied"))
	}
	size := 0
	switch head[3] {
	case 1:
		size = 4
	case 4:
		size = 16
	default:
		return fail(fmt.Errorf("unexpected SOCKS bound type"))
	}
	addr := make([]byte, size+2)
	_, e = io.ReadFull(c, addr)
	if e != nil {
		return fail(e)
	}
	ip := net.IP(addr[:size])
	if ip.IsUnspecified() {
		ip = net.ParseIP("127.0.0.1")
	}
	return c, &net.UDPAddr{IP: ip, Port: int(binary.BigEndian.Uint16(addr[size:]))}, nil
}

func probe() error {
	if e := probeTarget("tcp", "203.0.113.10"); e != nil {
		return e
	}
	return probeTarget("udp", "203.0.113.10")
}

func probeTarget(network, host string) error {
	payload := []byte("Alpine fixture TCP and UDP payload")
	if network == "tcp" {
		c, _, e := socks(1, host)
		if e != nil {
			return e
		}
		defer c.Close()
		_, e = c.Write(payload)
		if e != nil {
			return e
		}
		b := make([]byte, len(payload))
		_, e = io.ReadFull(c, b)
		if e != nil {
			return e
		}
		if !bytes.Equal(b, payload) {
			return fmt.Errorf("TCP echo differs")
		}
		return nil
	}
	control, relay, e := socks(3, host)
	if e != nil {
		return e
	}
	defer control.Close()
	u, e := net.DialUDP("udp", nil, relay)
	if e != nil {
		return e
	}
	defer u.Close()
	_ = u.SetDeadline(time.Now().Add(2 * time.Second))
	datagram := append([]byte{0, 0, 0, 1}, net.ParseIP(host).To4()...)
	datagram = append(datagram, 0x46, 0xa0)
	datagram = append(datagram, payload...)
	_, e = u.Write(datagram)
	if e != nil {
		return e
	}
	reply := make([]byte, 2048)
	n, e := u.Read(reply)
	if e != nil {
		return e
	}
	if n < 10 || !bytes.Equal(reply[n-len(payload):n], payload) {
		return fmt.Errorf("UDP echo differs")
	}
	return nil
}

func markProbe(host, markValue, group string) error {
	mark, e := strconv.ParseUint(markValue, 0, 32)
	if e != nil {
		return e
	}
	if e = os.WriteFile("/sys/fs/cgroup"+group+"/cgroup.procs", []byte(strconv.Itoa(os.Getpid())), 0600); e != nil {
		return e
	}
	d := net.Dialer{Timeout: time.Second, Control: func(_, _ string, c syscall.RawConn) error {
		var e error
		if err := c.Control(func(fd uintptr) { e = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark)) }); err != nil {
			return err
		}
		return e
	}}
	c, e := d.Dial("tcp", host+":18080")
	if e != nil {
		return e
	}
	return c.Close()
}
