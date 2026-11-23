package proxynode

import (
	"errors"
	"maps"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/mieruconfig"
	"ctlvps/internal/sshconfig"
	"ctlvps/internal/wgconfig"
	"gopkg.in/yaml.v3"
)

var proxyFields = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Fields("uuid password username cipher alterId alter-id network udp tls dialer-proxy servername sni skip-cert-verify fingerprint client-fingerprint alpn flow encryption reality-opts xhttp-opts ws-opts grpc-opts h2-opts http-opts transport obfs obfs-password obfs-opts psk version ports up down up-speed down-speed recv-window recv-window-conn congestion-controller udp-relay-mode reduce-rtt disable-sni fast-open tfo auth auth-str protocol protocol-param obfs-param plugin plugin-opts smux packet-encoding udp-over-tcp udp-over-tcp-version ip-version") {
		m[s] = true
	}
	return m
}()

func validateProxy(p Proxy) error {
	if len(p.Name) > 512 || len(p.Server) > 253 || strings.ContainsAny(p.Name, "\r\n\x00") || strings.ContainsAny(p.Server, "\r\n\x00, =") || p.Port < 0 || p.Port > 65535 {
		return errors.New("节点名称、地址或端口无效")
	}
	if p.Type == "ssh" {
		_, err := sshconfig.Decode(p.Params)
		return err
	}
	if p.Type == "wireguard" {
		_, err := wgconfig.Decode(p.Params)
		return err
	}
	if p.Type == "mieru" {
		_, err := mieruconfig.Decode(p.Params)
		return err
	}
	params := p.Params
	if list, ok := params["alpn"].([]string); ok {
		params = maps.Clone(params)
		params["alpn"] = stringValues(list)
	}
	if err := agentproto.ValidateParams(params, 0); err != nil {
		return err
	}
	if raw, exists := params["alpn"]; exists {
		list, ok := raw.([]any)
		if !ok {
			return errors.New("ALPN 必须为字符串列表")
		}
		for _, item := range list {
			if s, ok := item.(string); !ok || s == "" || strings.Contains(s, ",") {
				return errors.New("ALPN 列表项无效")
			}
		}
	}
	if via, exists := params["dialer-proxy"]; exists {
		if _, ok := via.(string); !ok {
			return errors.New("前置代理名必须为字符串")
		}
	}
	if _, exists := params["extra"]; exists && p.Str("network") == "xhttp" {
		return errors.New("不支持的 XHTTP extra")
	}
	if options, exists := params["xhttp-opts"]; exists {
		m, ok := options.(map[string]any)
		if !ok || p.Str("network") != "xhttp" {
			return errors.New("无效的 XHTTP 选项")
		}
		for key, value := range m {
			s, ok := value.(string)
			if !ok || (key != "path" && key != "host" && key != "mode") {
				return errors.New("不支持的 XHTTP 选项")
			}
			if key == "mode" && s != "" && s != "auto" && s != "packet-up" && s != "stream-up" && s != "stream-one" {
				return errors.New("不支持的 XHTTP mode")
			}
		}
	}
	if plugin, ok := p.Params["plugin"].(string); ok && plugin != "" && plugin != "obfs" && plugin != "v2ray-plugin" {
		return errors.New("不支持的客户端插件")
	}
	return nil
}

func boundedYAML(text string, out any) error {
	if len(text) > 16<<20 {
		return errors.New("订阅内容过大")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return errors.New("订阅 YAML 无效")
	}
	count := 0
	var visit func(*yaml.Node, int) error
	visit = func(n *yaml.Node, depth int) error {
		count++
		if depth > 32 || count > 100000 || n.Kind == yaml.AliasNode {
			return errors.New("订阅结构过深、过大或含别名引用")
		}
		for _, c := range n.Content {
			if err := visit(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(&root, 0); err != nil {
		return err
	}
	return root.Decode(out)
}

// Validate checks a node again before it is serialized for a client.
func Validate(p Proxy) error { return validateProxy(p) }
