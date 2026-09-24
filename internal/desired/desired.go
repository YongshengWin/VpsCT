// Package desired computes the declarative state each agent must converge to
// and persists it as numbered revisions.
package desired

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

// Default pinned core versions; overridable via settings.
const (
	DefaultSingBoxVersion = domain.DefaultSingBoxVersion
	DefaultSnellVersion   = domain.DefaultSnellVersion
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
	generation, err := b.Store.NetworkGeneration(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	networkVersion, err := b.Store.NetworkBindingVersion(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	egressVersion, err := b.Store.NetworkEgressVersion(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	wgVersion, err := b.Store.NetworkWireGuardVersion(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	mitaVersion, err := b.Store.MitaVersion(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	sshVersion, err := b.Store.NetworkSSHVersion(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	forwardVersion, err := b.Store.NetworkForwardVersion(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	if egressVersion > 0 || forwardVersion > 0 {
		networkVersion = max(networkVersion, agentproto.NetworkBindingVersion)
	}
	shares := map[int64]domain.Share{}
	versions, err := b.versions(ctx)
	if err != nil {
		return nil, err
	}
	ds := &agentproto.DesiredState{
		MitaVersion:             mitaVersion,
		NetworkSSHVersion:       sshVersion,
		NetworkWireGuardVersion: wgVersion,
		NetworkGeneration:       generation,
		NetworkBindingVersion:   networkVersion,
		NetworkEgressVersion:    egressVersion,
		NetworkForwardVersion:   forwardVersion,
		ServerID:                server.ID,
		ServerName:              server.Name,
		PublicHost:              server.PublicHost,
		CoreMode:                string(server.CoreMode),
		IPv4Only:                server.IPv4Only,
		Nodes:                   []agentproto.NodeSpec{},
		Versions:                versions,
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
	if mitaVersion == 0 {
		delete(ds.Versions, "mita")
	}
	if !server.Enabled {
		ds.Forwards, err = b.Store.DesiredForwards(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		for i := range ds.Forwards {
			ds.Forwards[i].Blocked = true
		}
		if len(ds.Forwards) > 0 {
			ds.NetworkForwardVersion = agentproto.NetworkForwardVersion
			ds.NetworkBindingVersion = agentproto.NetworkBindingVersion
			for _, f := range ds.Forwards {
				if !f.HasTransport() {
					continue
				}
				ds.NetworkEgressVersion = agentproto.NetworkEgressVersion
				if f.SSH != nil {
					ds.NetworkSSHVersion = agentproto.NetworkSSHVersion
				}
				if f.WireGuard != nil {
					ds.NetworkWireGuardVersion = agentproto.NetworkWireGuardVersion
				}
			}
		}
		// disabled server: converge to "nothing running"
		ds.Hash = Hash(ds)
		return ds, nil
	}
	nodes, err := b.Store.DeployedNodeNetworks(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	connlogAny := false
	selfLog := b.Store.GetSettingBool(ctx, domain.SettingConnlogSelf, true)
	for _, item := range nodes {
		n := item.Node
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
		if n.Network != nil {
			// A binding can be created between reading the sticky requirement
			// and the node snapshot; it still must carry the wire contract.
			ds.NetworkBindingVersion = agentproto.NetworkBindingVersion
			policy := *n.Network
			spec.Network = &agentproto.NodeNetworkSpec{Policy: policy}
			if policy.EgressProfileID != 0 {
				profile, revision := item.Profile, item.Revision
				if profile == nil || revision == nil || profile.ServerID != server.ID {
					return nil, fmt.Errorf("node %d egress profile is not usable by this server", n.ID)
				}
				switch profile.Kind {
				case "direct":
					direct, err := networkconfig.DecodeDirect(revision.Config)
					if err != nil {
						return nil, fmt.Errorf("node %d egress config: %w", n.ID, err)
					}
					spec.Network.Direct = &direct
				case "wireguard":
					cfg, err := networkconfig.DecodeWireGuard(revision.Config)
					if err != nil {
						return nil, err
					}
					if item.Credentials == nil || item.Credentials.ValidateWireGuard(cfg) != nil {
						return nil, fmt.Errorf("node %d WireGuard credentials unavailable", n.ID)
					}
					spec.Network.WireGuard = &agentproto.WireGuardEgress{Config: cfg, Credentials: *item.Credentials}
					ds.NetworkWireGuardVersion = agentproto.NetworkWireGuardVersion
					ds.NetworkEgressVersion = agentproto.NetworkEgressVersion
				case "ssh":
					cfg, err := networkconfig.DecodeSSH(revision.Config)
					if err != nil {
						return nil, err
					}
					if item.Credentials == nil || item.Credentials.ValidateSSH(cfg) != nil {
						return nil, fmt.Errorf("node %d SSH credentials unavailable", n.ID)
					}
					spec.Network.SSH = &agentproto.SSHEgress{Config: cfg, Credentials: *item.Credentials}
					ds.NetworkSSHVersion = agentproto.NetworkSSHVersion
					ds.NetworkEgressVersion = agentproto.NetworkEgressVersion
				case "socks5":
					cfg, err := networkconfig.DecodeSOCKS5(revision.Config)
					if err != nil {
						return nil, fmt.Errorf("node %d egress config: %w", n.ID, err)
					}
					transport := &agentproto.SOCKS5Egress{Config: cfg}
					if item.Credentials != nil {
						transport.Credentials = *item.Credentials
					}
					if err := transport.Credentials.Validate(cfg.Authentication); err != nil {
						return nil, fmt.Errorf("node %d egress credentials are not usable", n.ID)
					}
					spec.Network.SOCKS5 = transport
					ds.NetworkEgressVersion = agentproto.NetworkEgressVersion
				case "ss2022":
					if !corecompat.SS2022Outbound(versions["sing-box"].Version) {
						return nil, fmt.Errorf("node %d SS-2022 outbound requires official sing-box 1.14.1 or compatible 1.14.x", n.ID)
					}
					cfg, err := networkconfig.DecodeSS2022(revision.Config)
					if err != nil {
						return nil, fmt.Errorf("node %d SS-2022 config: %w", n.ID, err)
					}
					if item.Credentials == nil || item.Credentials.ValidateSS2022(cfg.Method) != nil {
						return nil, fmt.Errorf("node %d SS-2022 credentials unavailable", n.ID)
					}
					spec.Network.SS2022 = &agentproto.SS2022Egress{Config: cfg, Credentials: *item.Credentials}
					ds.NetworkEgressVersion = agentproto.NetworkEgressVersion
				default:
					return nil, fmt.Errorf("node %d egress kind is unsupported", n.ID)
				}
				if !profile.Enabled {
					spec.Blocked = true
				}
			}
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
	ds.Forwards, err = b.Store.DesiredForwards(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	// Retain historical UDP resources and their identities, but close their
	// listeners under official cores until idle expiry is qualified. Do not
	// reject the whole publication and leave an old UDP listener running.
	for i := range ds.Forwards {
		if ds.Forwards[i].Config.Network != "tcp" && !corecompat.UDPForward(versions["sing-box"].Version) {
			ds.Forwards[i].Blocked = true
		}
	}
	if len(ds.Forwards) > 0 {
		ds.NetworkForwardVersion = agentproto.NetworkForwardVersion
		ds.NetworkBindingVersion = agentproto.NetworkBindingVersion
		for _, f := range ds.Forwards {
			if !f.HasTransport() {
				continue
			}
			ds.NetworkEgressVersion = agentproto.NetworkEgressVersion
			if f.SSH != nil {
				ds.NetworkSSHVersion = agentproto.NetworkSSHVersion
			}
			if f.WireGuard != nil {
				ds.NetworkWireGuardVersion = agentproto.NetworkWireGuardVersion
			}
		}
	}
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

func (b *Builder) versions(ctx context.Context) (map[string]agentproto.CoreVersion, error) {
	sb := strings.TrimSpace(b.Store.GetSetting(ctx, domain.SettingSingBoxVersion, DefaultSingBoxVersion))
	if sb == "" {
		sb = DefaultSingBoxVersion
	}
	sn := strings.TrimSpace(b.Store.GetSetting(ctx, domain.SettingSnellVersion, DefaultSnellVersion))
	if sn == "" {
		sn = DefaultSnellVersion
	}
	mi := strings.TrimSpace(b.Store.GetSetting(ctx, domain.SettingMitaVersion, domain.DefaultMitaVersion))
	if mi == "" {
		mi = domain.DefaultMitaVersion
	}
	sums := b.Store.GetSetting(ctx, "core.mita_sha256", "")
	if sums == "" && mi == domain.DefaultMitaVersion {
		sums = domain.DefaultMitaSHA256
	}
	out := map[string]agentproto.CoreVersion{
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
		"mita": {Version: mi, URL: "https://github.com/enfein/mieru/releases/download/v{version}/mita_{version}_linux_{arch}.tar.gz", SHA256: parseSums(sums)},
	}
	return out, nil
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
	return b.publish(ctx, serverID, false)
}

// ForcePublish uses the same atomic publication path while assigning a new
// revision even when content is unchanged (an explicit administrator retry).
func (b *Builder) ForcePublish(ctx context.Context, serverID int64) (domain.DesiredState, bool, error) {
	return b.publish(ctx, serverID, true)
}

func (b *Builder) publish(ctx context.Context, serverID int64, force bool) (domain.DesiredState, bool, error) {
	for attempt := 0; attempt < 8; attempt++ {
		latest, err := b.Store.LatestDesiredState(ctx, serverID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return domain.DesiredState{}, false, err
		}
		// Server fields are inputs too: bracket their read as well as the
		// subsequent node/profile/settings reads with the same generation.
		generation, err := b.Store.NetworkGeneration(ctx, serverID)
		if err != nil {
			return domain.DesiredState{}, false, err
		}
		server, err := b.Store.GetServer(ctx, serverID)
		if err != nil {
			return domain.DesiredState{}, false, err
		}
		ds, err := b.Build(ctx, server)
		if err != nil {
			return domain.DesiredState{}, false, err
		}
		if generation != ds.NetworkGeneration {
			continue
		}
		ds.GeneratedAt = b.Now()
		rec, created, err := b.Store.PublishDesired(ctx, latest.Revision, ds, force)
		if errors.Is(err, store.ErrDesiredConflict) {
			continue
		}
		return rec, created, err
	}
	return domain.DesiredState{}, false, store.ErrDesiredConflict
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
