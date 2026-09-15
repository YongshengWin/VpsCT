package subscription

import (
	"encoding/base64"
	"strings"

	"ctlvps/internal/proxynode"
)

// RenderRaw produces a base64 encoded list of share links (V2rayN, NekoBox,
// Shadowrocket...). plain=true skips the base64 layer.
func RenderRaw(b *Bundle, plain bool) (*Rendered, error) {
	var lines []string
	if b.Userinfo != nil && b.InfoNodes != nil {
		// Shadowrocket shows a "STATUS=" comment line; harmless for others.
		lines = append(lines, "STATUS="+strings.Join(b.InfoNodes, " | "))
	}
	for _, p := range b.Proxies {
		if uri, err := proxynode.ToURI(p); err == nil {
			lines = append(lines, uri)
		}
	}
	// Chains cannot be expressed in share links; export the landing node only
	// under the chain name so the user still sees it.
	for _, c := range b.Chains {
		p := c.Proxy
		if uri, err := proxynode.ToURI(p); err == nil {
			lines = append(lines, uri)
		}
	}
	body := strings.Join(lines, "\n") + "\n"
	if plain {
		return &Rendered{Body: []byte(body), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".txt", Format: FormatURIList}, nil
	}
	return &Rendered{Body: []byte(base64.StdEncoding.EncodeToString([]byte(body))), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".txt", Format: FormatRaw}, nil
}
