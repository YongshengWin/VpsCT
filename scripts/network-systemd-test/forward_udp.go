//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/core"
	"ctlvps/internal/udpsession"
)

func udpSocketCount(pid int) int {
	fds, e := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	must(e)
	inodes := map[string]bool{}
	for _, fd := range fds {
		link, e := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if e == nil && strings.HasPrefix(link, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	n := 0
	for _, name := range []string{"udp", "udp6"} {
		body, e := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, name))
		must(e)
		for _, line := range strings.Split(string(body), "\n") {
			f := strings.Fields(line)
			if len(f) > 9 && inodes[f[9]] {
				n++
			}
		}
	}
	return n
}

func testUDPForwardSessions(ctx context.Context, address, source, want string, virtual bool) {
	sd := core.NewSystemd()
	var pid int
	var cfg udpsession.Config
	var api *udpsession.API
	eventually("UDP local manager ready and previous probes reaped", func() bool {
		var e error
		pid, cfg, api, e = core.LoadUDPManager(ctx, sd)
		if e != nil || api == nil {
			return false
		}
		items, e := api.Connections(ctx, cfg.Policies)
		return e == nil && len(items) == 0
	})
	baseline := udpSocketCount(pid)
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", "/fixtures/probe", "forward-client", "udp", address, source)
	input, e := cmd.StdinPipe()
	must(e)
	output, e := cmd.StdoutPipe()
	must(e)
	cmd.Stderr = os.Stderr
	must(cmd.Start())
	defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	decoder := json.NewDecoder(output)
	var reply struct {
		Value string `json:"value"`
		Error string `json:"error"`
	}
	must(decoder.Decode(&reply))
	if reply.Error != "" {
		panic("UDP client initialization failed")
	}
	request := func(command string) {
		_, e := io.WriteString(input, command+"\n")
		must(e)
		must(decoder.Decode(&reply))
		expected := want
		if command == "empty-send" {
			expected = "sent"
		}
		if command == "drain" {
			expected = "drained"
		}
		if reply.Error != "" || reply.Value != expected {
			panic(fmt.Sprintf("held UDP request failed: %+v", reply))
		}
	}
	request("probe")
	items, e := api.Connections(ctx, cfg.Policies)
	must(e)
	if len(items) != 1 {
		panic("held UDP connection not observed")
	}
	id := items[0].ID
	ip := netip.MustParseAddr(items[0].Metadata.SourceIP).Unmap()
	port, e := strconv.ParseUint(items[0].Metadata.SourcePort, 10, 16)
	must(e)
	var flow udpsession.Flow
	for f, p := range cfg.Policies {
		flow = udpsession.Flow{ForwardID: f, Source: netip.AddrPortFrom(ip, uint16(port)), ListenPort: p.ListenPort}
	}
	// The official direct inbound can close its mapping on a zero-payload
	// datagram. Kernel admission still counts it; the reaper must not stop the
	// core, and the next normal datagram must recreate a usable mapping.
	request("empty-send")
	live, e := udpsession.Live(ctx)
	must(e)
	if !live[flow] {
		panic("empty UDP packet lost kernel admission")
	}
	request("drain")
	request("probe")
	items, e = api.Connections(ctx, cfg.Policies)
	must(e)
	if len(items) != 1 {
		panic("normal UDP failed after empty datagram")
	}
	id = items[0].ID
	unchanged := func() {
		items, e := api.Connections(ctx, cfg.Policies)
		must(e)
		if len(items) != 1 || items[0].ID != id {
			live, e := udpsession.Live(ctx)
			must(e)
			panic(fmt.Sprintf("active UDP connection replaced: sessions=%d kernel_admitted=%t UDP_sockets=%d", len(items), live[flow], udpSocketCount(pid)))
		}
	}
	for i := 0; i < 12; i++ {
		time.Sleep(400 * time.Millisecond)
		request("probe")
		unchanged()
	}
	request("drain")
	request("push")
	for i := 0; i < 14; i++ {
		request("receive")
		unchanged()
	}
	if !virtual && udpSocketCount(pid) <= baseline {
		panic("outbound UDP socket was not observed")
	}
	// WireGuard uses a userspace UDP flow inside a shared tunnel socket;
	// expiring that flow must preserve the endpoint's transport socket.
	if virtual && udpSocketCount(pid) != baseline {
		panic("WireGuard unexpectedly changed shared transport sockets")
	}
	eventually("idle UDP API connection and outbound socket actually close", func() bool {
		items, e := api.Connections(ctx, cfg.Policies)
		return e == nil && len(items) == 0 && udpSocketCount(pid) == baseline
	})
	request("probe")
	items, e = api.Connections(ctx, cfg.Policies)
	must(e)
	if len(items) != 1 || items[0].ID == id {
		panic("expired UDP client did not get a fresh mapping")
	}
	fmt.Println("PASS real UDP lifecycle: normal and downstream-only traffic preserve mapping; empty datagram admission and normal recovery; idle connection closes with expected socket budget; same client recreates mapping")
}

func testUDPManagerFailure(ctx context.Context, probe func() bool, combined bool, want string) {
	sd := core.NewSystemd()
	_, _, api, e := core.LoadUDPManager(ctx, sd)
	must(e)
	if api == nil {
		panic("missing UDP manager for fault injection")
	}
	checkTCP := func() {}
	if combined {
		held := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", "/fixtures/probe", "forward-client", "tcp", "192.0.2.1:25001", "192.0.2.2")
		in, err := held.StdinPipe()
		must(err)
		out, err := held.StdoutPipe()
		must(err)
		must(held.Start())
		defer func() { in.Close(); held.Process.Kill(); held.Wait() }()
		d := json.NewDecoder(out)
		var reply struct{ Value, Error string }
		must(d.Decode(&reply))
		if reply.Error != "" || reply.Value != want {
			panic("held TCP setup failed")
		}
		checkTCP = func() {
			_, err := io.WriteString(in, "probe\n")
			must(err)
			must(d.Decode(&reply))
			if reply.Error != "" || reply.Value != want {
				panic("existing TCP connection disrupted by UDP fault")
			}
		}
	}
	_, port, _ := strings.Cut(api.Address, ":")
	run("nft", "add", "table", "inet", "fixture_udp_manager")
	defer func() { run("nft", "delete", "table", "inet", "fixture_udp_manager") }()
	run("nft", "add", "chain", "inet", "fixture_udp_manager", "output", "{ type filter hook output priority -156; policy accept; }")
	run("nft", "add", "rule", "inet", "fixture_udp_manager", "output", "ip", "daddr", "127.0.0.1", "tcp", "dport", port, "drop")
	pid, err := sd.PublicProxyPID(ctx)
	must(err)
	eventually("failed session manager fences UDP only", func() bool { return !probe() })
	checkTCP()
	current, err := sd.PublicProxyPID(ctx)
	must(err)
	if current != pid || !sd.IsActive(ctx, "ctlvps-singbox.service") {
		panic("UDP manager failure restarted shared core")
	}
	tcp := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", "/fixtures/probe", "forward-client", "tcp", "192.0.2.1:25001", "192.0.2.2")
	// Combined listeners prove TCP survives in the same exact process.
	if combined {
		out, err := tcp.Output()
		must(err)
		if !strings.Contains(string(out), fmt.Sprintf(`"value":%q`, want)) {
			panic("UDP fault disrupted TCP side")
		}
	}
	run("nft", "flush", "chain", "inet", "fixture_udp_manager", "output")
	eventually("UDP manager recovery reopens UDP without core restart", probe)
	checkTCP()
	current, err = sd.PublicProxyPID(ctx)
	must(err)
	if current != pid {
		panic("UDP recovery restarted shared core")
	}
	fmt.Println("PASS UDP manager failure fences UDP only, retains core/TCP; recovery clears sessions and reopens UDP without restart")
}
