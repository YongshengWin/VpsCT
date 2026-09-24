//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/auth"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"encoding/hex"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
)

func testNativeSubscriptions(ctx context.Context, st *store.Store, d *desired.Builder, shares *share.Manager, server domain.Server, admin *fixtureAdmin, apiServer *httptest.Server) {
	for bin, version := range map[string]string{"mita": "3.37.0", "snell-server": "5.0.1"} {
		copyFile("/fixtures/"+bin, "/opt/ctlvps/bin/"+bin, 0755)
		b, e := os.ReadFile("/opt/ctlvps/bin/" + bin)
		must(e)
		sum := sha256.Sum256(b)
		writeJSON("/opt/ctlvps/bin/"+bin+".trusted", map[string]string{"Version": version, "SHA256": hex.EncodeToString(sum[:])})
	}
	for _, protocol := range []string{"mieru", "snell"} {
		template := domain.RuleTemplate{Name: "native-export-" + protocol, Kind: "mihomo", Content: "mixed-port: 1080\nallow-lan: false\nmode: rule\nlog-level: error\nproxies: []\nproxy-groups:\n  - name: fixture\n    type: select\n    proxies: ['{{all}}']\nrules: ['MATCH,fixture']\n"}
		must(st.CreateTemplate(ctx, &template))
		var node domain.Node
		admin.request("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", server.ID), map[string]any{"protocol": protocol, "name": "native-" + protocol, "mieru_transport": "TCP"}, 201, &node)
		token := auth.NewSubscriptionToken()
		sub := domain.Subscription{Name: "native-generated", Kind: domain.SubGenerated, Enabled: true, Token: token, TokenHash: auth.HashToken(token), TemplateID: &template.ID, NodeSelection: domain.NodeSelection{NodeIDs: []int64{node.ID}}}
		must(st.CreateSubscription(ctx, &sub))
		sh := domain.Share{Name: "native-share-" + protocol, TemplateID: &template.ID, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{protocol}}}}
		shared, e := shares.Create(ctx, &sh)
		must(e)
		rec, e := st.LatestDesiredState(ctx, server.ID)
		must(e)
		eventually("native shared inbounds", func() bool {
			a, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && a.ApplyError == "" && a.AppliedRevision == rec.Revision
		})
		for _, source := range []struct{ kind, token string }{{"generated", token}, {"share", shared}} {
			res, e := apiServer.Client().Get(apiServer.URL + "/s/" + source.token + "/mihomo")
			must(e)
			body, e := io.ReadAll(res.Body)
			res.Body.Close()
			must(e)
			if res.StatusCode != 200 {
				panic("public native subscription failed")
			}
			path := "/tmp/native-subscription.yaml"
			must(os.WriteFile(path, body, 0600))
			stop := process(ctx, "ip", "netns", "exec", "landing", "/fixtures/mihomo", "-d", "/tmp/native-mihomo", "-f", path)
			eventually(source.kind+" "+protocol+" from public export", func() bool { got, e := probe(0, "tcp", "203.0.113.10"); return e == nil && got == "192.0.2.1" })
			for _, transport := range []string{"tcp", "udp", "greeting"} {
				got, e := probe(0, transport, "203.0.113.10")
				if e != nil || got != "192.0.2.1" {
					stop()
					panic(fmt.Sprintf("native %s %s %s failed", source.kind, protocol, transport))
				}
			}
			stop()
			fmt.Println("PASS public", source.kind, "template -> official mihomo ->", protocol, "TCP/UDP/server-first")
		}
		must(shares.Delete(ctx, sh.ID))
		admin.request("DELETE", fmt.Sprintf("/api/v1/nodes/%d", node.ID), nil, 204, nil)
	}
}
