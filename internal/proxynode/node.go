// Package proxynode converts proxy nodes between share URIs, Clash/mihomo
// proxy maps and the persistent domain.Node representation. Clash field
// names are the canonical parameter vocabulary.
package proxynode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/mieruconfig"
	"ctlvps/internal/sshconfig"
	"ctlvps/internal/wgconfig"
)

// Proxy is an in-memory, Clash-shaped node: name/type/server/port plus a
// free-form parameter map with Clash keys.
type Proxy struct {
	Name   string
	Type   string
	Server string
	Port   int
	Params map[string]any
	// Region metadata is used only during subscription generation and is never
	// serialized into client proxy parameters.
	Region       string
	RegionSource string
}

// FromDomain unpacks a domain.Node.
func FromDomain(n domain.Node) Proxy {
	p := Proxy{Name: n.Name, Type: n.Protocol, Server: n.Server, Port: n.Port, Params: map[string]any{}}
	if len(n.Params) > 0 {
		_ = json.Unmarshal(n.Params, &p.Params)
	}
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	return p
}

// ToDomain packs into a domain.Node (id/ownership left to the caller).
func (p Proxy) ToDomain() domain.Node {
	params := p.Params
	if params == nil {
		params = map[string]any{}
	}
	b, _ := json.Marshal(params)
	return domain.Node{Name: p.Name, Protocol: p.Type, Server: p.Server, Port: p.Port, Params: b, Enabled: true, Tags: []string{}}
}

// ClashMap renders the proxy as a Clash/mihomo proxy entry.
func (p Proxy) ClashMap() map[string]any {
	m := map[string]any{"name": p.Name, "type": p.Type, "server": p.Server, "port": p.Port}
	for k, v := range p.Params {
		if k == "name" || k == "type" || k == "server" || k == "port" {
			continue
		}
		m[k] = v
	}
	return m
}

// FromClashMap builds a Proxy from a Clash proxy entry.
func FromClashMap(m map[string]any) (Proxy, error) {
	p := Proxy{Params: map[string]any{}}
	p.Name = str(m["name"])
	p.Type = strings.ToLower(str(m["type"]))
	p.Server = str(m["server"])
	p.Port = toInt(m["port"])
	if p.Type == "" || p.Server == "" {
		return p, fmt.Errorf("proxy %q: missing type/server", p.Name)
	}
	if p.Type == "hysteria2" && p.Port == 0 && str(m["ports"]) == "" {
		return p, fmt.Errorf("proxy %q: missing port", p.Name)
	}
	if p.Type != "hysteria2" && p.Port == 0 {
		return p, fmt.Errorf("proxy %q: missing port", p.Name)
	}
	if p.Name == "" {
		p.Name = fmt.Sprintf("%s-%s:%d", p.Type, p.Server, p.Port)
	}
	for k, v := range m {
		switch k {
		case "name", "type", "server", "port":
			continue
		}
		if k == "extra" && m["network"] == "xhttp" {
			return p, fmt.Errorf("不支持的 XHTTP extra")
		}
		if p.Type == "ssh" && !sshconfig.Field(k) {
			return p, fmt.Errorf("SSH 节点包含不支持的字段")
		}
		if p.Type == "wireguard" && !wgconfig.Field(k) || p.Type == "mieru" && !mieruconfig.Field(k) {
			return p, fmt.Errorf("%s 节点包含不支持的字段", p.Type)
		}
		if proxyFields[k] || (p.Type == "ssh" && sshconfig.Field(k)) || p.Type == "wireguard" && wgconfig.Field(k) || p.Type == "mieru" && mieruconfig.Field(k) {
			p.Params[k] = normalizeYAML(v)
		}
	}
	if p.Type == "wireguard" {
		c, err := wgconfig.Decode(p.Params)
		if err != nil {
			return p, err
		}
		p.Params = c.Params()
	}
	if p.Type == "mieru" {
		c, err := mieruconfig.Decode(p.Params)
		if err != nil {
			return p, err
		}
		p.Params = c.Params()
	}
	if err := validateProxy(p); err != nil {
		return p, err
	}
	return p, nil
}

// normalizeYAML converts yaml.v3 decoded maps (map[string]any already) and
// leaves other values untouched; kept for symmetry with JSON round trips.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[any]any:
		out := map[string]any{}
		for k, vv := range t {
			out[fmt.Sprint(k)] = normalizeYAML(vv)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, vv := range t {
			out[k] = normalizeYAML(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normalizeYAML(vv)
		}
		return out
	}
	return v
}

// BasicWebSocket returns the handshake fields a single-Host client can express.
// Do not silently drop early data, additional headers, or other WS options.
func (p Proxy) BasicWebSocket() (path, host string, ok bool) {
	opts := p.Sub("ws-opts")
	if _, exists := p.Params["ws-opts"]; exists && opts == nil {
		return "", "", false
	}
	for k, v := range opts {
		switch k {
		case "path":
			if path, ok = v.(string); !ok {
				return "", "", false
			}
		case "headers":
			h, ok := v.(map[string]any)
			if !ok || len(h) > 1 {
				return "", "", false
			}
			for name, value := range h {
				if host, ok = value.(string); !ok || !strings.EqualFold(name, "Host") {
					return "", "", false
				}
			}
		default:
			return "", "", false
		}
	}
	return path, host, true
}

// BasicGRPCService represents the ordinary single-stream gRPC transport only.
func (p Proxy) BasicGRPCService() (string, bool) {
	o := p.Sub("grpc-opts")
	if _, exists := p.Params["grpc-opts"]; exists && o == nil {
		return "", false
	}
	if len(o) == 0 {
		return "", true
	}
	s, ok := o["grpc-service-name"].(string)
	return s, len(o) == 1 && ok
}

// URI and YAML imports use the same JSON-compatible list representation.
// Keep one entry beyond ValidateParams' bound so an oversized list is rejected,
// not truncated into an acceptable one or unnecessarily materialized in full.
const overParamListLimit = 2049

func splitList(value string) []any {
	return stringValues(strings.SplitN(value, ",", overParamListLimit))
}

func stringValues(values []string) []any {
	if len(values) > overParamListLimit {
		values = values[:overParamListLimit]
	}
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// Str returns a string parameter.
func (p Proxy) Str(key string) string { return str(p.Params[key]) }

// Bool returns a boolean parameter.
func (p Proxy) Bool(key string) bool { return toBool(p.Params[key]) }

// Int returns an integer parameter.
func (p Proxy) Int(key string) int { return toInt(p.Params[key]) }

// Sub returns a nested map parameter (e.g. ws-opts).
func (p Proxy) Sub(key string) map[string]any {
	if m, ok := p.Params[key].(map[string]any); ok {
		return m
	}
	return nil
}

// StrList returns a list-of-strings parameter (e.g. alpn).
func (p Proxy) StrList(key string) []string {
	switch t := p.Params[key].(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			out = append(out, str(v))
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return strings.Split(t, ",")
	}
	return nil
}

// Set assigns a parameter, dropping empty strings / zero values to keep the
// map tidy.
func (p *Proxy) Set(key string, v any) {
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return
		}
	case int:
		if t == 0 {
			return
		}
	case bool:
		if !t {
			return
		}
	case []string:
		if len(t) == 0 {
			return
		}
	case map[string]any:
		if len(t) == 0 {
			return
		}
	case nil:
		return
	}
	p.Params[key] = v
}

// HostPort renders server:port with IPv6 brackets.
func (p Proxy) HostPort() string {
	if strings.Contains(p.Server, ":") && !strings.HasPrefix(p.Server, "[") {
		return "[" + p.Server + "]:" + strconv.Itoa(p.Port)
	}
	return p.Server + ":" + strconv.Itoa(p.Port)
}

// SortedKeys is a helper for deterministic output.
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return fmt.Sprint(v)
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes" || s == "on"
	case int:
		return t != 0
	case float64:
		return t != 0
	}
	return false
}
