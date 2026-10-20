package nft

import (
	"encoding/json"
	"strconv"
	"strings"
)

// nft JSON hides xt_multiport's ports. Read the compatibility frontend without
// changing it, and cross-check the two views. Native expressions use the
// destination-port proof below; opaque xt rules retain the strict jump shape.
func compatInputDisjoint(c *ingressEntry, rules []*ingressEntry, ports map[string]map[int]bool, listing string) bool {
	if c.Policy == "accept" && (c.Family == "inet" || c.Family == "ip" || c.Family == "ip6") && chainDestinationPortsDisjoint(rules, ports) {
		return true
	}
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

// Only read-only matches and counters may precede the proven destination-port
// restriction. Source ports describe the remote client and prove nothing about
// the local listener. Unknown expressions fail closed, even after the match.
func chainDestinationPortsDisjoint(rules []*ingressEntry, ports map[string]map[int]bool) bool {
	if len(rules) == 0 {
		return false
	}
	for _, rule := range rules {
		proven, verdict := false, false
		for _, expression := range rule.Expr {
			if len(expression) != 1 || verdict {
				return false
			}
			for kind, raw := range expression {
				switch kind {
				case "match":
					var match struct {
						Op    string                     `json:"op"`
						Left  map[string]json.RawMessage `json:"left"`
						Right json.RawMessage            `json:"right"`
					}
					if json.Unmarshal(raw, &match) != nil || len(match.Left) != 1 || len(match.Right) == 0 || match.Op != "==" {
						return false
					}
					if payload, ok := match.Left["payload"]; ok {
						var p struct {
							Protocol string `json:"protocol"`
							Field    string `json:"field"`
						}
						if json.Unmarshal(payload, &p) != nil {
							return false
						}
						switch {
						case (p.Protocol == "tcp" || p.Protocol == "udp") && p.Field == "dport":
							if destinationSetDisjoint(match.Right, ports[p.Protocol]) {
								proven = true
							} else {
								return false
							}
						case (p.Protocol == "ip" || p.Protocol == "ip6") && (p.Field == "saddr" || p.Field == "daddr"):
							// Additional address predicates can only narrow the rule.
						default:
							return false
						}
					} else if meta, ok := match.Left["meta"]; ok {
						var m struct {
							Key string `json:"key"`
						}
						var proto string
						if json.Unmarshal(meta, &m) != nil || m.Key != "l4proto" || json.Unmarshal(match.Right, &proto) != nil || (proto != "tcp" && proto != "udp") {
							return false
						}
					} else {
						return false
					}
				case "counter":
				case "accept", "drop", "reject", "jump", "goto", "return":
					if !proven {
						return false
					}
					verdict = true
				default:
					return false
				}
			}
		}
		if !proven || !verdict {
			return false
		}
	}
	return true
}

func destinationSetDisjoint(raw json.RawMessage, ports map[int]bool) bool {
	if n, err := strconv.Atoi(string(raw)); err == nil {
		return n >= 1 && n <= 65535 && !ports[n]
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
		return false
	}
	if value, ok := object["range"]; ok {
		var bounds []int
		if json.Unmarshal(value, &bounds) != nil || len(bounds) != 2 || bounds[0] < 1 || bounds[1] > 65535 || bounds[0] > bounds[1] {
			return false
		}
		for p := range ports {
			if p >= bounds[0] && p <= bounds[1] {
				return false
			}
		}
		return true
	}
	if value, ok := object["set"]; ok {
		var members []json.RawMessage
		if json.Unmarshal(value, &members) != nil || len(members) == 0 {
			return false
		}
		for _, member := range members {
			// nft inline sets contain port numbers or ranges, not nested sets.
			var nested map[string]json.RawMessage
			if json.Unmarshal(member, &nested) == nil {
				if _, ok := nested["range"]; !ok {
					return false
				}
			}
			if !destinationSetDisjoint(member, ports) {
				return false
			}
		}
		return true
	}
	return false
}
