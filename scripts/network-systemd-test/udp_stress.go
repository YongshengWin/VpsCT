//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/store"
	"ctlvps/internal/udpsession"
)

func processRSS(pid int) int64 {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	must(err)
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			n, e := strconv.ParseInt(f[1], 10, 64)
			must(e)
			return n * 1024
		}
	}
	panic("RSS unavailable")
}

func testUDPStress(ctx context.Context, st *store.Store, serverID int64) {
	seconds, err := strconv.Atoi(os.Getenv("UDP_STRESS_SECONDS"))
	must(err)
	if seconds < 30 || seconds > 720 {
		panic("stress duration outside fixture budget")
	}
	sd := core.NewSystemd()
	var pid int
	var cfg udpsession.Config
	var api *udpsession.API
	eventually("stress UDP manager", func() bool {
		var e error
		pid, cfg, api, e = core.LoadUDPManager(ctx, sd)
		return e == nil && api != nil && len(cfg.Policies) == 8
	})
	// A configured systemd property is insufficient when the container parent
	// never enabled the memory controller. Require the actual kernel limit.
	cgroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	must(err)
	memoryGroup := ""
	for _, line := range strings.Split(string(cgroup), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			memoryGroup = filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"))
		}
	}
	if memoryGroup == "" {
		panic("stress fixture requires cgroup v2 memory enforcement")
	}
	limit, err := os.ReadFile(filepath.Join(memoryGroup, "memory.max"))
	if err != nil {
		panic(fmt.Sprintf("stress memory controller unavailable; container must enable Docker memory limits: %v", err))
	}
	if strings.TrimSpace(string(limit)) != "268435456" {
		panic("stress fixture proxy memory.max must enforce the product's 256MiB budget: " + string(limit))
	}
	fmt.Printf("UDP stress kernel budget verified: %s memory.max=%s", memoryGroup, limit)
	runtimeEnv, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	must(err)
	thpBound := false
	for _, value := range strings.Split(string(runtimeEnv), "\x00") {
		thpBound = thpBound || value == "GODEBUG=disablethp=1"
	}
	if !thpBound {
		panic("stress proxy must use the product's process-local THP workaround")
	}
	args := []string{"netns", "exec", "landing", "/fixtures/probe", "udp-load", "8192"}
	for port := 25001; port <= 25008; port++ {
		args = append(args, fmt.Sprintf("192.0.2.1:%d", port))
	}
	cmd := exec.CommandContext(ctx, "ip", args...)
	in, e := cmd.StdinPipe()
	must(e)
	out, e := cmd.StdoutPipe()
	must(e)
	cmd.Stderr = os.Stderr
	must(cmd.Start())
	defer func() { in.Close(); cmd.Process.Kill(); cmd.Wait() }()
	decoder := json.NewDecoder(out)
	diagnostics := func(label string) {
		if a, err := st.GetAgentByServer(ctx, serverID); err == nil {
			var d agentproto.Diagnostics
			_ = json.Unmarshal(a.Diagnostics, &d)
			fmt.Printf("UDP stress %s: manager=%+v proxy_rss=%dKiB\n", label, d.ForwardUDP, processRSS(pid)/1024)
		}
		for _, file := range []string{"/proc/net/snmp", "/sys/fs/cgroup/ctlvps-proxy.slice/memory.current", "/sys/fs/cgroup/ctlvps-proxy.slice/memory.events"} {
			if body, err := os.ReadFile(file); err == nil {
				fmt.Printf("UDP stress %s: %s: %s\n", label, file, body)
			}
		}
		for _, name := range []string{"smaps_rollup", "cgroup"} {
			if body, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, name)); err == nil {
				fmt.Printf("UDP stress %s: proxy %s: %s\n", label, name, body)
				if name == "cgroup" {
					for _, line := range strings.Split(string(body), "\n") {
						if path, ok := strings.CutPrefix(line, "0::"); ok {
							for _, metric := range []string{"memory.current", "memory.max", "memory.events"} {
								file := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"), metric)
								if value, err := os.ReadFile(file); err == nil {
									fmt.Printf("UDP stress %s: %s: %s\n", label, file, value)
								}
							}
						}
					}
				}
			}
		}
		if env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
			for _, item := range strings.Split(string(env), "\x00") {
				if strings.HasPrefix(item, "GOMEMLIMIT=") {
					fmt.Printf("UDP stress %s: %s\n", label, item)
				}
			}
		}
	}
	var result struct {
		Ready, Active, Failed int
		Millis                int64
	}
	must(decoder.Decode(&result))
	if result.Ready != 8192 {
		panic("load clients not ready")
	}
	round := func(n int) {
		_, e := io.WriteString(in, fmt.Sprintln(n))
		must(e)
		must(decoder.Decode(&result))
		if result.Failed != 0 {
			diagnostics("request failure")
			panic(fmt.Sprintf("UDP stress lost %d/%d requests (round %dms)", result.Failed, n, result.Millis))
		}
	}
	round(8192)
	items, e := api.Connections(ctx, cfg.Policies)
	must(e)
	if len(items) != 8192 {
		panic(fmt.Sprintf("expected8192 live sessions, got%d", len(items)))
	}
	original := map[string]bool{}
	for _, c := range items {
		original[c.ID] = true
	}
	startRSS, peakRSS := processRSS(pid), processRSS(pid)
	diagnostics("capacity reached")
	begin := time.Now()
	lastProgress := begin
	rounds := 0
	for time.Since(begin) < time.Duration(seconds)*time.Second {
		round(8192)
		rounds++
		current, e := sd.PublicProxyPID(ctx)
		must(e)
		if current != pid {
			panic("stress restarted core")
		}
		items, e = api.Connections(ctx, cfg.Policies)
		must(e)
		if len(items) != 8192 {
			panic("stress unexpectedly reaped live sessions")
		}
		for _, c := range items {
			if !original[c.ID] {
				panic("active UDP mapping replaced during stress")
			}
		}
		rss := processRSS(pid)
		peakRSS = max(peakRSS, rss)
		if rss > 384<<20 {
			diagnostics("memory failure")
			panic(fmt.Sprintf("stress proxy RSS exceeded384MiB budget: start=%dKiB peak=%dKiB round=%dms", startRSS/1024, peakRSS/1024, result.Millis))
		}
		memoryEvents, err := os.ReadFile(filepath.Join(memoryGroup, "memory.events"))
		must(err)
		for _, line := range strings.Split(string(memoryEvents), "\n") {
			if value, ok := strings.CutPrefix(line, "oom_kill "); ok && value != "0" {
				panic("stress proxy cgroup reported OOM kill: " + value)
			}
		}
		if time.Since(lastProgress) >= 15*time.Second {
			fmt.Printf("UDP stress progress: elapsed=%ds sessions=%d round=%dms proxy_rss=%dKiB\n", int(time.Since(begin).Seconds()), len(items), result.Millis, rss/1024)
			lastProgress = time.Now()
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Preserve64 active clients while the other8128 expire, then refill all8192.
	agentBefore, err := st.GetAgentByServer(ctx, serverID)
	must(err)
	var before agentproto.Diagnostics
	must(json.Unmarshal(agentBefore.Diagnostics, &before))
	if before.ForwardUDP == nil {
		panic("stress baseline diagnostics missing")
	}
	baselineReaped := before.ForwardUDP.Reaped
	deadline := time.Now().Add(60 * time.Second)
	for {
		round(64)
		items, e = api.Connections(ctx, cfg.Policies)
		must(e)
		for _, c := range items {
			if !original[c.ID] {
				panic("active UDP mapping replaced during idle batch")
			}
		}
		if len(items) == 64 {
			break
		}
		if time.Now().After(deadline) {
			panic(fmt.Sprintf("idle stress capacity not reclaimed: %d", len(items)))
		}
		time.Sleep(200 * time.Millisecond)
	}
	round(8192)
	items, e = api.Connections(ctx, cfg.Policies)
	must(e)
	if len(items) != 8192 {
		panic("reclaimed capacity cannot refill")
	}
	eventually("stress diagnostics reach panel", func() bool {
		// Keep the refilled sessions active while waiting for a heartbeat;
		// that wait can exceed the eight-second idle policy.
		round(8192)
		a, e := st.GetAgentByServer(ctx, serverID)
		var d agentproto.Diagnostics
		_ = json.Unmarshal(a.Diagnostics, &d)
		if d.ForwardUDP != nil && d.ForwardUDP.Reaped > baselineReaped+8128 {
			panic("expiry batch reaped active clients")
		}
		return e == nil && d.ForwardUDP != nil && d.ForwardUDP.State == "healthy" && d.ForwardUDP.Reaped == baselineReaped+8128
	})
	fmt.Printf("PASS UDP stress:8192 sessions, %d seconds,%d rounds,no active mapping replacement;8128 idle sessions reclaimed and capacity refilled; proxyRSS start=%dKiB peak=%dKiB final=%dKiB\n", seconds, rounds, startRSS/1024, peakRSS/1024, processRSS(pid)/1024)
}
