package subscription

import (
	"fmt"
	"strings"
)

// Policy names may be quoted and contain delimiters; keep the original tokens
// when rewriting a group (in particular URLs and policy-path options).
func textFields(s string, separator byte) []string {
	var fields []string
	quoted, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			quoted = !quoted
		case s[i] == separator && !quoted:
			fields = append(fields, s[start:i])
			start = i + 1
		}
	}
	return append(fields, s[start:])
}
func textName(s string) string { return strings.Trim(strings.TrimSpace(s), `"`) }
func textAssignment(line string) (name, value string, ok bool) {
	parts := textFields(line, '=')
	if len(parts) < 2 {
		return "", "", false
	}
	return textName(parts[0]), strings.Join(parts[1:], "="), true
}
func textSection(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
		return strings.ToLower(line[1 : len(line)-1])
	}
	return ""
}

// Overlay matching group names, retaining template policies still used by its
// own rules (replacing the entire section leaves the built-in FINAL dangling).
func mergeTextGroups(src string, groups []string) string {
	overrides := map[string]bool{}
	for _, g := range groups {
		name, _, _ := textAssignment(g)
		overrides[name] = true
	}
	lines := strings.Split(src, "\n")
	var out []string
	inGroup, found := false, false
	for _, line := range lines {
		if section := textSection(line); section != "" {
			inGroup = section == "proxy group"
			out = append(out, line)
			if inGroup {
				found = true
				out = append(out, groups...)
			}
			continue
		}
		if inGroup {
			if name, _, ok := textAssignment(line); ok && overrides[name] {
				continue
			}
		}
		out = append(out, line)
	}
	if !found {
		out = append(out, "[Proxy Group]")
		out = append(out, groups...)
	}
	return strings.Join(out, "\n")
}

// Remove filtered members and unusable groups, but never drop/retarget a rule:
// that could make its traffic reach the next rule (including FINAL,DIRECT).
func checkTextPolicies(src string, omitted map[string]bool, expectedProxies []string) (string, error) {
	lines := strings.Split(src, "\n")
	declared, actualProxies := map[string]bool{}, map[string]bool{}
	groups := map[string]int{}
	var rules []int
	section := ""
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if sec := textSection(s); sec != "" {
			section = sec
			continue
		}
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") || strings.HasPrefix(s, "//") {
			continue
		}
		switch section {
		case "proxy", "proxy group":
			if name, _, ok := textAssignment(line); ok {
				declared[name] = true
				if section == "proxy group" {
					groups[name] = i
				} else {
					actualProxies[name] = true
				}
			}
		case "rule":
			rules = append(rules, i)
		}
	}
	for _, name := range expectedProxies {
		if !actualProxies[name] {
			return "", fmt.Errorf("%w：文本模板没有在 [Proxy] 中写入订阅节点", ErrNoRenderPath)
		}
	}
	deleted := map[int]bool{}
	for changed := true; changed; {
		changed = false
		for name, i := range groups {
			if deleted[i] {
				continue
			}
			_, value, _ := textAssignment(lines[i])
			f := textFields(value, ',')
			kept := []string{f[0]}
			n := 0
			dynamic := false
			if !oneOf(strings.ToLower(strings.TrimSpace(f[0])), "select", "url-test", "fallback", "load-balance", "smart") {
				continue
			} // e.g. subnet/SSID have another policy grammar
			for _, field := range f[1:] {
				member := textName(field)
				if opt, _, ok := strings.Cut(strings.TrimSpace(field), "="); ok && !strings.HasPrefix(strings.TrimSpace(field), `"`) {
					if oneOf(strings.ToLower(opt), "policy-path", "include-all-proxies", "include-other-group") {
						dynamic = true
					}
					kept = append(kept, field)
					continue
				}
				if omitted[member] || member == "" {
					continue
				}
				if !declared[member] && !builtinPolicies[member] {
					return "", fmt.Errorf("%w：文本代理组 %q 引用不可用的策略 %q", ErrNoRenderPath, name, member)
				}
				kept = append(kept, field)
				n++
			}
			if n == 0 && !dynamic {
				deleted[i] = true
				omitted[name] = true
				changed = true
				continue
			}
			if len(kept) != len(f) {
				lines[i] = surgeIdent(name) + " =" + strings.Join(kept, ",")
			}
		}
	}
	for _, i := range rules {
		f := textFields(lines[i], ',')
		if len(f) < 2 {
			continue
		}
		j := len(f) - 1
		for j > 1 && oneOf(strings.ToLower(strings.TrimSpace(f[j])), "no-resolve", "extended-matching", "pre-matching", "force-remote-dns") {
			j--
		}
		target := textName(f[j])
		if omitted[target] || !declared[target] && !builtinPolicies[target] {
			return "", fmt.Errorf("%w：文本规则引用不可用的策略 %q", ErrNoRenderPath, target)
		}
	}
	if len(deleted) > 0 {
		kept := lines[:0]
		for i, line := range lines {
			if !deleted[i] {
				kept = append(kept, line)
			}
		}
		lines = kept
	}
	return strings.Join(lines, "\n"), nil
}
