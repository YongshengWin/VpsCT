//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// An opt-in fixture client, never included in release binaries.
func udpLoad() {
	count, err := strconv.Atoi(os.Args[2])
	must(err)
	if count < 1 || count > 8192 || len(os.Args) != 11 {
		panic("invalid load fixture")
	}
	var clients []*net.UDPConn
	for i := 0; i < count; i++ {
		target, err := net.ResolveUDPAddr("udp", os.Args[3+i%8])
		must(err)
		c, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("192.0.2.2")}, target)
		must(err)
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	encoder := json.NewEncoder(os.Stdout)
	must(encoder.Encode(map[string]any{"ready": count}))
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 1 {
			panic("invalid load command")
		}
		active, err := strconv.Atoi(fields[0])
		must(err)
		if active < 0 || active > count {
			panic("invalid load count")
		}
		started := time.Now()
		jobs := make(chan int)
		var wg sync.WaitGroup
		var mu sync.Mutex
		failed := 0
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var buf [64]byte
				for index := range jobs {
					c := clients[index]
					_ = c.SetDeadline(time.Now().Add(2 * time.Second))
					_, e := c.Write([]byte("probe\n"))
					n := 0
					if e == nil {
						n, e = c.Read(buf[:])
					}
					if e != nil || string(buf[:n]) != "198.51.100.1" {
						mu.Lock()
						failed++
						mu.Unlock()
					}
				}
			}()
		}
		for i := 0; i < active; i++ {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		must(encoder.Encode(map[string]any{"active": active, "failed": failed, "millis": time.Since(started).Milliseconds(), "ready": count}))
	}
	must(scanner.Err())
	fmt.Fprintln(os.Stderr, "UDP load client stopped")
}
