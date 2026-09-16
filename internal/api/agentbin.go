package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentproto"
)

type agentBinMeta struct {
	Path   string
	SHA256 string
	Size   int64
	Mod    time.Time
}

var agentBinCache struct {
	mu sync.Mutex
	by map[string]agentBinMeta
}

func normalizeAgentArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "amd64", "x86_64", "x64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return ""
	}
}

func (a *API) agentBinaryCandidates(platform string) []string {
	var out []string
	if a.Config.AgentBinDir != "" {
		out = append(out, filepath.Join(a.Config.AgentBinDir, "ctlvps-agent-"+platform))
	}
	if a.Config.DataDir != "" {
		out = append(out, filepath.Join(a.Config.DataDir, "agents", "ctlvps-agent-"+platform))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "ctlvps-agent-"+platform))
	}
	return out
}

func (a *API) findAgentBinary(platform string) string {
	for _, p := range a.agentBinaryCandidates(platform) {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (a *API) agentBinaryMeta(arch string) (agentBinMeta, bool) {
	arch = normalizeAgentArch(arch)
	if arch == "" {
		return agentBinMeta{}, false
	}
	platform := "linux-" + arch
	path := a.findAgentBinary(platform)
	if path == "" {
		return agentBinMeta{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return agentBinMeta{}, false
	}
	agentBinCache.mu.Lock()
	defer agentBinCache.mu.Unlock()
	if agentBinCache.by == nil {
		agentBinCache.by = map[string]agentBinMeta{}
	}
	if hit, ok := agentBinCache.by[path]; ok && hit.Mod.Equal(st.ModTime()) && hit.Size == st.Size() {
		return hit, true
	}
	f, err := os.Open(path)
	if err != nil {
		return agentBinMeta{}, false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return agentBinMeta{}, false
	}
	meta := agentBinMeta{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Size: st.Size(), Mod: st.ModTime()}
	agentBinCache.by[path] = meta
	return meta, true
}

func (a *API) agentUpdateSpec(arch string) *agentproto.AgentUpdateSpec {
	meta, ok := a.agentBinaryMeta(arch)
	if !ok {
		return nil
	}
	return &agentproto.AgentUpdateSpec{
		SHA256: meta.SHA256,
		URL:    "/dl/agent/linux-" + normalizeAgentArch(arch),
		Size:   meta.Size,
	}
}

func agentReportedSHA(hb agentproto.Heartbeat, diag agentproto.Diagnostics) string {
	if hb.BinarySHA256 != "" {
		return hb.BinarySHA256
	}
	return diag.BinarySHA256
}
