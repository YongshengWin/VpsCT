package nft

import (
	"encoding/json"
	"strconv"
	"strings"
)

// nft JSON hides xt_multiport's ports. Read the compatibility frontend without
// changing it, and cross-check the two views. Only this exact port-scoped jump
// shape is understood; unknown matches/actions and default-drop remain errors.
func compatInputDisjoint(c *ingressEntry, rules []*ingressEntry, ports map[string]map[int]bool, listing string) bool {
	if (c.Family != "ip" && c.Family != "ip6") || c.Table != "filter" || c.Name != "INPUT" || c.Policy != "accept" || listing == "" {
		return false
	}
	lines := strings.Split(strings.TrimSpace(listing), "\n")
	if len(lines) != len(rules)+1 || lines[0] != "-P INPUT ACCEPT" {
		return false
	}
	for i, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) != 10 || f[0] != "-A" || f[1] != "INPUT" || f[2] != "-p" || (f[3] != "tcp" && f[3] != "udp") || f[4] != "-m" || f[5] != "multiport" || f[6] != "--dports" || f[8] != "-j" {
			return false
		}
		// No protocol or port overlap means even a restrictive target cannot
		// affect these nodes. Never rely on a fail2ban chain's name or current bans.
		for _, item := range strings.Split(f[7], ",") {
			bounds := strings.Split(item, ":")
			if len(bounds) > 2 {
				return false
			}
			lo, e := strconv.Atoi(bounds[0])
			if e != nil || lo < 1 || lo > 65535 {
				return false
			}
			hi := lo
			if len(bounds) == 2 {
				hi, e = strconv.Atoi(bounds[1])
				if e != nil || hi < lo || hi > 65535 {
					return false
				}
			}
			for p := range ports[f[3]] {
				if p >= lo && p <= hi {
					return false
				}
			}
		}
		expr := rules[i].Expr
		if len(expr) != 4 {
			return false
		}
		var match struct {
			Op   string `json:"op"`
			Left struct {
				Meta struct {
					Key string `json:"key"`
				} `json:"meta"`
			} `json:"left"`
			Right string `json:"right"`
		}
		var xt struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		var jump struct {
			Target string `json:"target"`
		}
		if len(expr[0]) != 1 || json.Unmarshal(expr[0]["match"], &match) != nil || match.Op != "==" || match.Left.Meta.Key != "l4proto" || match.Right != f[3] {
			return false
		}
		if len(expr[1]) != 1 || json.Unmarshal(expr[1]["xt"], &xt) != nil || xt.Type != "match" || xt.Name != "multiport" {
			return false
		}
		if _, ok := expr[2]["counter"]; len(expr[2]) != 1 || !ok {
			return false
		}
		if len(expr[3]) != 1 || json.Unmarshal(expr[3]["jump"], &jump) != nil || jump.Target != f[9] {
			return false
		}
	}
	return true
}
