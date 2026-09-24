package agentnet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/safehttp"
	"golang.org/x/net/dns/dnsmessage"
)

type DNSResult struct {
	Addresses  []string `json:"addresses"`
	TTLSeconds uint32   `json:"ttl_seconds"`
	Truncated  bool     `json:"truncated,omitempty"`
}

type dnsRequest struct {
	Host, Family, Transport string
}

// QueryDNS passes only a connected socket and a bounded query to a dedicated
// unprivileged process. The caller owns binding, destination permission and
// accounting. DNS wire parsing never runs in the privileged coordinator.
func QueryDNS(ctx context.Context, conn net.Conn, host, family, transport string) (DNSResult, error) {
	var empty DNSResult
	r := dnsRequest{host, family, transport}
	if err := r.validate(); err != nil {
		return empty, err
	}
	fileConn, ok := conn.(interface{ File() (*os.File, error) })
	if !ok {
		return empty, errors.New("DNS 查询需要已连接的本机 socket")
	}
	fd, err := fileConn.File()
	if err != nil {
		return empty, err
	}
	defer fd.Close()
	body, _ := json.Marshal(r)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, networkExecutable(), "network-dns")
	cmd.Stdin = bytes.NewReader(body)
	cmd.ExtraFiles = []*os.File{fd}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "GOMAXPROCS=1", "GOMEMLIMIT=" + agentbudget.NetworkGoLimit}
	if err = isolate(cmd); err != nil {
		return empty, err
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return empty, err
	}
	if err = cmd.Start(); err != nil {
		return empty, err
	}
	raw, readErr := safehttp.ReadBounded(pipe, 4096)
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		return empty, errors.New("受限启动 DNS 查询失败或超时")
	}
	var out DNSResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || len(out.Addresses) > 8 || (out.Truncated && (len(out.Addresses) != 0 || transport != "udp")) {
		return empty, errors.New("启动 DNS 子进程返回无效结果")
	}
	for _, address := range out.Addresses {
		ip, err := networkconfig.HostAddress(address)
		if err != nil || ip.Is4() != (family == "ipv4") {
			return empty, errors.New("启动 DNS 子进程返回无效地址")
		}
	}
	return out, nil
}

func (r dnsRequest) validate() error {
	if networkconfig.ValidateAdvertiseHost(r.Host) != nil || len(r.Host) > 253 || (r.Family != "ipv4" && r.Family != "ipv6") || (r.Transport != "tcp" && r.Transport != "udp") {
		return errors.New("启动 DNS 查询字段无效")
	}
	if _, err := netip.ParseAddr(r.Host); err == nil {
		return errors.New("启动 DNS 只接受域名")
	}
	return nil
}

func dnsEntry(args []string) (bool, error) {
	if len(args) != 1 || args[0] != "network-dns" {
		return false, nil
	}
	if os.Geteuid() == 0 {
		return true, errors.New("DNS 子进程不得以 root 运行")
	}
	if err := harden(); err != nil {
		return true, err
	}
	raw, err := safehttp.ReadBounded(os.Stdin, 1024)
	if err != nil {
		return true, err
	}
	var r dnsRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.validate() != nil {
		return true, errors.New("无效启动 DNS 查询")
	}
	file := os.NewFile(3, "dns-socket")
	if file == nil {
		return true, errors.New("缺少启动 DNS socket")
	}
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return true, err
	}
	defer conn.Close()
	_, udp := conn.(*net.UDPConn)
	_, tcp := conn.(*net.TCPConn)
	if (r.Transport == "udp" && !udp) || (r.Transport == "tcp" && !tcp) {
		return true, errors.New("启动 DNS socket 类型不一致")
	}
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return true, err
	}
	out, err := lookupDNS(conn, r)
	if err != nil {
		return true, errors.New("启动 DNS 响应无效或查询失败")
	}
	return true, json.NewEncoder(os.Stdout).Encode(out)
}

func lookupDNS(conn net.Conn, r dnsRequest) (DNSResult, error) {
	var out DNSResult
	typ := dnsmessage.TypeA
	if r.Family == "ipv6" {
		typ = dnsmessage.TypeAAAA
	}
	host := strings.ToLower(strings.TrimSuffix(r.Host, ".")) + "."
	seen := map[string]bool{}
	ttl := ^uint32(0)
	for hops := 0; hops < 8; hops++ {
		if seen[host] {
			return out, errors.New("DNS alias cycle")
		}
		seen[host] = true
		name, err := dnsmessage.NewName(host)
		if err != nil {
			return out, err
		}
		var nonce [2]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return out, err
		}
		question := dnsmessage.Question{Name: name, Type: typ, Class: dnsmessage.ClassINET}
		query := dnsmessage.Message{Header: dnsmessage.Header{ID: binary.BigEndian.Uint16(nonce[:]), RecursionDesired: true}, Questions: []dnsmessage.Question{question}}
		wire, err := query.Pack()
		if err != nil {
			return out, err
		}
		if r.Transport == "tcp" {
			wire = append([]byte{byte(len(wire) >> 8), byte(len(wire))}, wire...)
		}
		written, err := conn.Write(wire)
		if err != nil {
			return out, err
		}
		if written != len(wire) {
			return out, io.ErrShortWrite
		}
		reply := make([]byte, 16384)
		if r.Transport == "tcp" {
			var size [2]byte
			if _, err = io.ReadFull(conn, size[:]); err != nil {
				return out, err
			}
			n := int(binary.BigEndian.Uint16(size[:]))
			if n < 12 || n > len(reply) {
				return out, errors.New("DNS response size")
			}
			reply = reply[:n]
			_, err = io.ReadFull(conn, reply)
		} else {
			n, e := conn.Read(reply)
			err = e
			if n == len(reply) {
				return out, errors.New("DNS datagram limit")
			}
			reply = reply[:n]
		}
		if err != nil {
			return out, err
		}
		var parser dnsmessage.Parser
		header, err := parser.Start(reply)
		if err != nil || !header.Response || header.ID != query.ID || header.OpCode != 0 {
			return out, errors.New("DNS response identity")
		}
		q, err := parser.Question()
		if err != nil || !strings.EqualFold(q.Name.String(), host) || q.Type != typ || q.Class != dnsmessage.ClassINET {
			return out, errors.New("DNS question mismatch")
		}
		if _, err = parser.Question(); err != dnsmessage.ErrSectionDone {
			return out, errors.New("DNS question count")
		}
		if header.Truncated {
			if r.Transport != "udp" {
				return out, errors.New("truncated TCP DNS")
			}
			return DNSResult{Truncated: true}, nil
		}
		if header.RCode == dnsmessage.RCodeNameError {
			return out, nil
		}
		if header.RCode != dnsmessage.RCodeSuccess {
			return out, errors.New("DNS response error")
		}
		answers := map[string][]dnsmessage.Resource{}
		for count := 0; ; count++ {
			rr, err := parser.Answer()
			if err == dnsmessage.ErrSectionDone {
				break
			}
			if err != nil || count >= 128 {
				return out, errors.New("DNS answer budget")
			}
			if rr.Header.Class == dnsmessage.ClassINET {
				key := strings.ToLower(rr.Header.Name.String())
				answers[key] = append(answers[key], rr)
			}
		}
		next := ""
		for _, rr := range answers[host] {
			var ip netip.Addr
			switch data := rr.Body.(type) {
			case *dnsmessage.AResource:
				if typ == dnsmessage.TypeA {
					ip = netip.AddrFrom4(data.A)
				}
			case *dnsmessage.AAAAResource:
				if typ == dnsmessage.TypeAAAA {
					ip = netip.AddrFrom16(data.AAAA)
				}
			case *dnsmessage.CNAMEResource:
				alias := strings.ToLower(data.CNAME.String())
				if next != "" && next != alias {
					return out, errors.New("conflicting aliases")
				}
				next = alias
				ttl = min(ttl, rr.Header.TTL)
			}
			if ip.IsValid() {
				if len(out.Addresses) >= 8 {
					return out, errors.New("DNS address budget")
				}
				out.Addresses = append(out.Addresses, ip.String())
				ttl = min(ttl, rr.Header.TTL)
			}
		}
		if len(out.Addresses) > 0 {
			if next != "" {
				return DNSResult{}, errors.New("alias and address conflict")
			}
			out.TTLSeconds = ttl
			return out, nil
		}
		if next == "" {
			return out, nil
		}
		// Re-query only the alias through the same connected resolver. Never
		// trust unrelated additional records or accept another resolver address.
		host = next
	}
	return out, errors.New("DNS alias budget")
}
