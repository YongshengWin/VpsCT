// Package desired computes the declarative state each agent must converge to
// and persists it as numbered revisions.
package desired

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

// Default pinned core versions; overridable via settings.
const (
	DefaultSingBoxVersion = "1.12.14"
	DefaultSnellVersion   = "5.0.1"
)

// Builder turns store contents into agentproto.DesiredState.
type Builder struct {
	Store *store.Store
	Now   func() time.Time
}

// New constructs a Builder.
func New(st *store.Store) *Builder {
	return &Builder{Store: st, Now: func() time.Time { return time.Now().UTC() }}
}

// Build computes the desired state for a server without persisting it.
func (b *Builder) Build(ctx context.Context, server domain.Server) (*agentproto.DesiredState, error) {
	nodes, err := b.Store.ListNodes(ctx, store.NodeFilter{ServerID: &server.ID, Source: domain.NodeDeployed, IncludeRevoked: true})
	if err != nil {
		return nil, err
	}
	shares := map[int64]domain.Share{}
	ds := &agentproto.DesiredState{
		ServerID:   server.ID,
		ServerName: server.Name,
		PublicHost: server.PublicHost,
		CoreMode:   string(server.CoreMode),
		IPv4Only:   server.IPv4Only,
		Nodes:      []agentproto.NodeSpec{},
		Versions:   b.versions(ctx),
		Connlog: agentproto.ConnlogSpec{
			BatchSize:   500,
			FlushSec:    30,
			MaxBufferMB: 64,
		},
		Tuning: agentproto.Tuning{
			EnableBBR:    true,
			MemoryMaxMB:  256,
			LimitNOFILE:  1048576,
			Chrony:       true,
			RestartSec:   3,
			GoMemLimitMB: 192,
		},
	}
	if !server.Enabled {
		// disabled server: converge to "nothing running"
		ds.Hash = Hash(ds)
		return ds, nil
	}
	connlogAny := false
	selfLog := b.Store.GetSettingBool(ctx, domain.SettingConnlogSelf, true)
	for _, n := range nodes {
		if n.Revoked {
			continue
		}
		spec := agentproto.NodeSpec{
			NodeID:     n.ID,
			Name:       n.Name,
			Protocol:   n.Protocol,
			Core:       string(domain.CoreFor(n.Protocol, server.CoreMode)),
			ListenPort: n.ListenPort,
			ShareID:    n.ShareID,
			Blocked:    !n.Enabled,
			Params:     map[string]any{},
		}
		if len(n.ServerParams) > 0 {
			_ = json.Unmarshal(n.ServerParams, &spec.Params)
		}
		if n.ShareID != nil {
			sh, ok := shares[*n.ShareID]
			if !ok {
				if got, err := b.Store.GetShare(ctx, *n.ShareID); err == nil {
					sh, ok = got, true
					shares[sh.ID] = sh
				}
			}
			if ok {
				if sh.Status != domain.ShareActive {
					spec.Blocked = true
				}
				spec.ConnlogEnabled = sh.ConnlogEnabled
				if sh.ConnlogEnabled {
					connlogAny = true
				}
			}
		} else if selfLog {
			spec.ConnlogEnabled = true
			connlogAny = true
		}
		if needsCert(n.Protocol) {
			mode := fmt.Sprint(spec.Params["cert_mode"])
			if mode == "" || mode == "<nil>" {
				mode = server.CertMode
			}
			dom := fmt.Sprint(spec.Params["tls_domain"])
			if dom == "" || dom == "<nil>" {
				dom = server.PublicHost
			}
			spec.Cert = &agentproto.CertSpec{Mode: mode, Domain: dom}
			if mode == "external" {
				spec.Cert.ID = fmt.Sprint(spec.Params["cert_id"])

			}
		}
		ds.Nodes = append(ds.Nodes, spec)
	}
	ds.Connlog.Enabled = connlogAny
	ds.Hash = Hash(ds)
	return ds, nil
}

func needsCert(protocol string) bool {
	switch protocol {
	case domain.ProtocolAnyTLS, domain.ProtocolHysteria2, domain.ProtocolTUIC, domain.ProtocolTrojan:
		return true
	}
	return false
}

func (b *Builder) versions(ctx context.Context) map[string]agentproto.CoreVersion {
	sb := strings.TrimSpace(b.Store.GetSetting(ctx, domain.SettingSingBoxVersion, DefaultSingBoxVersion))
	if sb == "" {
		sb = DefaultSingBoxVersion
	}
	sn := strings.TrimSpace(b.Store.GetSetting(ctx, domain.SettingSnellVersion, DefaultSnellVersion))
	if sn == "" {
		sn = DefaultSnellVersion
	}
	return map[string]agentproto.CoreVersion{
		"sing-box": {
			Version: sb,
			URL:     "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-{arch}.tar.gz",
			SHA256:  parseSums(b.Store.GetSetting(ctx, "core.singbox_sha256", "")),
		},
		"snell-server": {
			Version: sn,
			URL:     "https://dl.nssurge.com/snell/snell-server-v{version}-linux-{snellarch}.zip",
			SHA256:  parseSums(b.Store.GetSetting(ctx, "core.snell_sha256", "")),
		},
	}
}

// parseSums parses "amd64=hex,arm64=hex".
func parseSums(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k != "" && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Hash returns a stable hash of the state content (excluding revision/hash/time).
func Hash(ds *agentproto.DesiredState) string {
	cp := *ds
	cp.Revision = 0
	cp.Hash = ""
	cp.GeneratedAt = time.Time{}
	b, _ := json.Marshal(cp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Publish builds the state and stores a new revision when it changed.
// Returns the current (possibly pre-existing) revision and whether it is new.
func (b *Builder) Publish(ctx context.Context, serverID int64) (domain.DesiredState, bool, error) {
	server, err := b.Store.GetServer(ctx, serverID)
	if err != nil {
		return domain.DesiredState{}, false, err
	}
	ds, err := b.Build(ctx, server)
	if err != nil {
		return domain.DesiredState{}, false, err
	}
	latest, err := b.Store.LatestDesiredState(ctx, serverID)
	if err == nil && latest.Hash == ds.Hash {
		return latest, false, nil
	}
	ds.GeneratedAt = b.Now()
	payload, _ := json.Marshal(ds)
	rec, err := b.Store.CreateDesiredState(ctx, serverID, payload, ds.Hash)
	if err != nil {
		return domain.DesiredState{}, false, err
	}
	// embed the assigned revision in the payload the agent will download
	ds.Revision = rec.Revision
	payload, _ = json.Marshal(ds)
	rec.Payload = payload
	if err := b.Store.UpdateDesiredPayload(ctx, rec.ID, payload); err != nil {
		return rec, true, err
	}
	return rec, true, nil
}

// PublishAll republishes every server (used after global setting changes).
func (b *Builder) PublishAll(ctx context.Context) error {
	servers, err := b.Store.ListServers(ctx)
	if err != nil {
		return err
	}
	for _, s := range servers {
		if _, _, err := b.Publish(ctx, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// Load decodes the payload of a stored revision.
func Load(rec domain.DesiredState) (*agentproto.DesiredState, error) {
	var ds agentproto.DesiredState
	if err := json.Unmarshal(rec.Payload, &ds); err != nil {
		return nil, err
	}
	ds.Revision = rec.Revision
	if ds.Hash == "" {
		ds.Hash = rec.Hash
	}
	return &ds, nil
}
