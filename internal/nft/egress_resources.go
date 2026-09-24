package nft

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"ctlvps/internal/agentproto"
)

type egressGroup struct {
	path, core    string
	private, acme bool
	marks         []string
}

func resourceEgressGroups(nodes []agentproto.NodeSpec, groups map[int64]string, forwards []agentproto.ForwardSpec, forwardGroup string) ([]egressGroup, error) {
	byPath := map[string]*egressGroup{}
	add := func(path, core string, private bool) (*egressGroup, error) {
		path = strings.TrimPrefix(path, "/")
		if path == "" || strings.Contains(path, "..") || strings.ContainsAny(path, "\"\\\r\n\t ") || (core != "singbox" && core != "snell" && core != "mieru") {
			return nil, errors.New("missing or invalid proxy cgroup")
		}
		g := byPath[path]
		if g == nil {
			g = &egressGroup{path: path, core: core, private: private}
			byPath[path] = g
		}
		if g.core != core || g.private != private {
			return nil, errors.New("inconsistent proxy permission group")
		}
		return g, nil
	}
	for _, n := range nodes {
		if n.Retired {
			continue
		}
		g, err := add(groups[n.NodeID], n.Core, n.AllowPrivate)
		if err != nil {
			return nil, err
		}
		if n.Core == "singbox" && !n.Blocked {
			mark, err := NodeMark(n.NodeID)
			if err != nil {
				return nil, err
			}
			g.marks = append(g.marks, fmt.Sprintf("0x%08x", mark))
			g.acme = g.acme || (n.Cert != nil && n.Cert.Mode == "acme")
		}
	}
	if forwardGroup != "" || len(forwards) > 0 {
		g, err := add(forwardGroup, "singbox", false)
		if err != nil {
			return nil, err
		}
		for _, f := range forwards {
			if err := f.Validate(); err != nil {
				return nil, err
			}
			if f.Retired || f.Blocked {
				continue
			}
			mark, _ := (agentproto.ResourceIdentity{Kind: "forward", ID: f.ForwardID}).Mark()
			g.marks = append(g.marks, fmt.Sprintf("0x%08x", mark))
		}
	}
	var out []egressGroup
	for _, g := range byPath {
		sort.Strings(g.marks)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}
