package subscription

import (
	"encoding/base64"
	"strings"
)

// RenderRaw produces a base64 encoded list of share links (V2rayN, NekoBox,
// Shadowrocket...). plain=true skips the base64 layer.
func RenderRaw(b *Bundle, plain bool) (*Rendered, error) {
	var lines []string
	if b.Userinfo != nil && b.InfoNodes != nil {
		// Shadowrocket shows a "STATUS=" comment line; harmless for others.
		lines = append(lines, "STATUS="+strings.Join(b.InfoNodes, " | "))
	}
	// Independent links cannot express a required front. Never turn the
	// landing proxy into an apparently equivalent stand-alone link.
	var names []string
	for _, p := range b.Proxies {
		if uri, err := StandaloneURI(p); err == nil {
			lines = append(lines, uri)
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 && !b.ValidationOnly {
		return nil, noRenderPath(FormatURIList)
	}
	body := strings.Join(lines, "\n") + "\n"
	if plain {
		return &Rendered{EmittedNames: names, Body: []byte(body), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".txt", Format: FormatURIList}, nil
	}
	return &Rendered{EmittedNames: names, Body: []byte(base64.StdEncoding.EncodeToString([]byte(body))), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".txt", Format: FormatRaw}, nil
}
