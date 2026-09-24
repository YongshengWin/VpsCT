package agentnet

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// net.Pipe keeps parser tests independent of host DNS and socket permissions.
func dnsExchange(t *testing.T, family, transport string, respond func(int, dnsmessage.Message) dnsmessage.Message) (DNSResult, error) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_ = server.SetDeadline(time.Now().Add(time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for step := 0; ; step++ {
			wire := make([]byte, 4096)
			if transport == "tcp" {
				var size [2]byte
				if _, err := io.ReadFull(server, size[:]); err != nil {
					return
				}
				n := int(binary.BigEndian.Uint16(size[:]))
				if n > len(wire) {
					return
				}
				wire = wire[:n]
				if _, err := io.ReadFull(server, wire); err != nil {
					return
				}
			} else {
				n, err := server.Read(wire)
				if err != nil {
					return
				}
				wire = wire[:n]
			}
			var q dnsmessage.Message
			if err := q.Unpack(wire); err != nil {
				t.Error(err)
				return
			}
			answer := respond(step, q)
			wire, err := answer.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if transport == "tcp" {
				wire = append([]byte{byte(len(wire) >> 8), byte(len(wire))}, wire...)
			}
			if _, err := server.Write(wire); err != nil {
				return
			}
		}
	}()
	out, err := lookupDNS(client, dnsRequest{"upstream.example", family, transport})
	client.Close()
	<-done
	return out, err
}

func dnsResponse(q dnsmessage.Message) dnsmessage.Message {
	q.Response = true
	return q
}

func dnsRecord(name string, ttl uint32, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Class: dnsmessage.ClassINET, TTL: ttl}, Body: body}
}

func TestBootstrapDNSAnswersAndAliases(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		t.Run(transport, func(t *testing.T) {
			out, err := dnsExchange(t, "ipv4", transport, func(step int, q dnsmessage.Message) dnsmessage.Message {
				a := dnsResponse(q)
				if step == 0 {
					a.Answers = []dnsmessage.Resource{dnsRecord("upstream.example.", 30, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("alias.example.")}), dnsRecord("alias.example.", 1, &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})}
				} else {
					if step != 1 || q.Questions[0].Name.String() != "alias.example." {
						t.Error("alias did not use a fresh query on the same connection")
					}
					a.Answers = []dnsmessage.Resource{dnsRecord("alias.example.", 60, &dnsmessage.AResource{A: [4]byte{192, 0, 2, 5}})}
				}
				return a
			})
			if err != nil || out.TTLSeconds != 30 || !reflect.DeepEqual(out.Addresses, []string{"192.0.2.5"}) {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
	out, err := dnsExchange(t, "ipv6", "tcp", func(_ int, q dnsmessage.Message) dnsmessage.Message {
		a := dnsResponse(q)
		a.Answers = []dnsmessage.Resource{dnsRecord("upstream.example.", 0, &dnsmessage.AAAAResource{AAAA: [16]byte{0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}})}
		return a
	})
	if err != nil || out.TTLSeconds != 0 || !reflect.DeepEqual(out.Addresses, []string{"2001:db8::1"}) {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestBootstrapDNSRejectsUnrelatedOrMalformedAnswers(t *testing.T) {
	cases := []struct {
		name      string
		change    func(*dnsmessage.Message)
		wantError bool
	}{
		{"wrong-id", func(q *dnsmessage.Message) { q.ID++ }, true},
		{"not-response", func(q *dnsmessage.Message) { q.Response = false }, true},
		{"wrong-question", func(q *dnsmessage.Message) { q.Questions[0].Name = dnsmessage.MustNewName("other.example.") }, true},
		{"two-questions", func(q *dnsmessage.Message) { q.Questions = append(q.Questions, q.Questions[0]) }, true},
		{"server-failure", func(q *dnsmessage.Message) { q.RCode = dnsmessage.RCodeServerFailure }, true},
		{"tcp-truncated", func(q *dnsmessage.Message) { q.Truncated = true }, true},
		{"nxdomain", func(q *dnsmessage.Message) { q.RCode = dnsmessage.RCodeNameError; q.Answers = nil }, false},
		{"unrelated-address", func(q *dnsmessage.Message) { q.Answers[0].Header.Name = dnsmessage.MustNewName("other.example.") }, false},
		{"wrong-class", func(q *dnsmessage.Message) { q.Answers[0].Header.Class = dnsmessage.ClassCHAOS }, false},
		{"wrong-family", func(q *dnsmessage.Message) { q.Answers[0].Body = &dnsmessage.AAAAResource{} }, false},
		{"alias-and-address", func(q *dnsmessage.Message) {
			q.Answers = append(q.Answers, dnsRecord("upstream.example.", 60, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("alias.example.")}))
		}, true},
		{"address-budget", func(q *dnsmessage.Message) {
			for len(q.Answers) < 9 {
				q.Answers = append(q.Answers, q.Answers[0])
			}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := dnsExchange(t, "ipv4", "tcp", func(_ int, q dnsmessage.Message) dnsmessage.Message {
				q = dnsResponse(q)
				q.Answers = []dnsmessage.Resource{dnsRecord("upstream.example.", 60, &dnsmessage.AResource{A: [4]byte{192, 0, 2, 5}})}
				tc.change(&q)
				return q
			})
			if (err != nil) != tc.wantError || (!tc.wantError && len(out.Addresses) != 0) {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
	for _, cycle := range []bool{true, false} {
		_, err := dnsExchange(t, "ipv4", "tcp", func(step int, q dnsmessage.Message) dnsmessage.Message {
			q = dnsResponse(q)
			alias := fmt.Sprintf("alias%d.example.", step)
			if cycle {
				alias = "upstream.example."
			}
			q.Answers = []dnsmessage.Resource{dnsRecord(q.Questions[0].Name.String(), 60, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(alias)})}
			return q
		})
		if err == nil {
			t.Fatal("unbounded alias chain accepted")
		}
	}
	out, err := dnsExchange(t, "ipv4", "udp", func(_ int, q dnsmessage.Message) dnsmessage.Message { q = dnsResponse(q); q.Truncated = true; return q })
	if err != nil || !out.Truncated || len(out.Addresses) != 0 {
		t.Fatalf("truncation: %+v %v", out, err)
	}
}
