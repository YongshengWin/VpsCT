//go:build linux

// Optional upstream reproducer: official SS2022 inbound and client with an
// immediate-greeting origin, without VpsCT, routing guards or an external network.
// Nonzero exit preserves interoperability failures rather than retrying them.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func save(path string, v any) {
	b, err := json.Marshal(v)
	must(err)
	must(os.WriteFile(path, b, 0600))
}
func waitPort(port string) {
	for i := 0; i < 100; i++ {
		c, e := net.DialTimeout("tcp", port, 20*time.Millisecond)
		if e == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	panic("not ready")
}
func probe(target []byte) error {
	c, e := net.DialTimeout("tcp", "127.0.0.1:1080", time.Second)
	if e != nil {
		return e
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	_, e = c.Write([]byte{5, 1, 0})
	if e != nil {
		return e
	}
	var auth [2]byte
	if _, e = io.ReadFull(c, auth[:]); e != nil {
		return e
	}
	if auth != [2]byte{5, 0} {
		return fmt.Errorf("auth %v", auth)
	}
	_, e = c.Write(append([]byte{5, 1, 0}, target...))
	if e != nil {
		return e
	}
	var reply [4]byte
	if _, e = io.ReadFull(c, reply[:]); e != nil {
		return e
	}
	if reply[1] != 0 {
		return fmt.Errorf("reply %v", reply)
	}
	n := 4
	if reply[3] == 4 {
		n = 16
	} else if reply[3] == 3 {
		var size [1]byte
		if _, e = io.ReadFull(c, size[:]); e != nil {
			return e
		}
		n = int(size[0])
	}
	if _, e = io.ReadFull(c, make([]byte, n+2)); e != nil {
		return e
	}
	line, e := bufio.NewReader(c).ReadString('\n')
	if e != nil {
		return e
	}
	if line != "fixture\n" {
		return fmt.Errorf("wrong greeting %q", line)
	}
	return nil
}

func main() {
	if os.Getenv("CTLVPS_SS2022_INTEROP_FIXTURE") != "1" {
		panic("explicit fixture required")
	}
	if _, e := os.Stat("/.dockerenv"); e != nil {
		panic("isolated container required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, check := range []struct{ path, arg, version string }{{"/fixture/sing-box", "version", "sing-box version 1.14.1"}, {"/fixture/sslocal", "--version", "1.25.0"}} {
		b, e := exec.CommandContext(ctx, check.path, check.arg).CombinedOutput()
		must(e)
		if !strings.Contains(string(b), check.version) {
			panic("unexpected fixture binary version")
		}
	}
	dir, e := os.MkdirTemp("/tmp", "ss-early-")
	must(e)
	defer os.RemoveAll(dir)
	key := make([]byte, 16)
	_, e = rand.Read(key)
	must(e)
	password := base64.StdEncoding.EncodeToString(key)
	logger := filepath.Join(dir, "server.log")
	server := filepath.Join(dir, "server.json")
	save(server, map[string]any{"log": map[string]any{"level": "error", "output": logger}, "dns": map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "fixture-hosts", "predefined": map[string]any{"localhost": []string{"127.0.0.1"}}}}}, "route": map[string]any{"default_domain_resolver": "fixture-hosts"}, "inbounds": []any{map[string]any{"type": "shadowsocks", "listen": "127.0.0.1", "listen_port": 21001, "method": "2022-blake3-aes-128-gcm", "password": password}}, "outbounds": []any{map[string]any{"type": "direct"}}})
	client := filepath.Join(dir, "client.json")
	save(client, map[string]any{"server": "127.0.0.1", "server_port": 21001, "method": "2022-blake3-aes-128-gcm", "password": password, "local_address": "127.0.0.1", "local_port": 1080})
	var wg sync.WaitGroup
	l, e := net.Listen("tcp", ":18080")
	must(e)
	defer l.Close()
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				c.Write([]byte("fixture\n"))
				io.Copy(io.Discard, c)
			}()
		}
	}()
	clientArgs := []string{"/fixture/sslocal", "-c", client}
	if os.Getenv("SS2022_TEST_CLIENT") == "sing-box" {
		save(client, map[string]any{"log": map[string]any{"level": "error", "output": filepath.Join(dir, "client.log")}, "inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": 1080}}, "outbounds": []any{map[string]any{"type": "shadowsocks", "server": "127.0.0.1", "server_port": 21001, "method": "2022-blake3-aes-128-gcm", "password": password}}})
		clientArgs = []string{"/fixture/sing-box", "run", "-c", client}
	}
	var children []*exec.Cmd
	for _, args := range [][]string{{"/fixture/sing-box", "run", "-c", server}, clientArgs} {
		log, e := os.Create(filepath.Join(dir, filepath.Base(args[0])+".stderr"))
		must(e)
		defer log.Close()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr = log, log
		must(cmd.Start())
		children = append(children, cmd)
	}
	defer func() {
		for _, c := range children {
			c.Process.Kill()
			c.Wait()
		}
	}()
	waitPort("127.0.0.1:1080")
	waitPort("127.0.0.1:21001")
	// Literal and domain targets both use the same loopback origin. No VpsCT,
	// nftables guard, maintenance lock, or simulated controller is involved.
	literal := []byte{1, 127, 0, 0, 1, 70, 160}
	domain := append([]byte{3, 9}, []byte("localhost")...)
	domain = append(domain, 70, 160)
	var failures, completed atomic.Int64
	jobs := make(chan int, 2000)
	for i := 0; i < 2000; i++ {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if failures.Load() >= 3 || ctx.Err() != nil {
					return
				}
				target := literal
				if i%2 != 0 {
					target = domain
				}
				if e := probe(target); e != nil {
					fmt.Printf("FAIL iteration %d domain=%v: %v\n", i, i%2 != 0, e)
					failures.Add(1)
				}
				if n := completed.Add(1); n%250 == 0 {
					fmt.Printf("completed %d connections\n", n)
				}
			}
		}()
	}
	workers.Wait()
	fmt.Printf("completed stress; connections=%d failures=%d\n", completed.Load(), failures.Load())
	for _, name := range []string{"server.log", "client.log", "sslocal.stderr"} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if len(b) > 16384 {
			b = b[len(b)-16384:]
		}
		b = bytes.ReplaceAll(b, []byte(password), []byte("[redacted]"))
		fmt.Printf("%s:\n%s\n", name, b)
	}
	l.Close()
	wg.Wait()
	if failures.Load() > 0 || completed.Load() != 2000 {
		os.Exit(1)
	}
}
