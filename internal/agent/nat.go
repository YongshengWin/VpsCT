package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/agentwork"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/nft"
	"ctlvps/internal/secureupdate"
)

// Local authority is checked before any HTTP request, so revoking or repairing
// a mapping on a running host does not depend on controller connectivity.
func (a *Agent) refreshLocalNAT(ctx context.Context) {
	if agentwork.Pending() || a.maintenanceAction() != "" {
		return
	}
	if natMutationAllowed() != nil {
		return
	}
	release, err := lockConfiguration()
	if err != nil {
		return
	}
	defer release()
	a.mu.Lock()
	ds := a.desired
	a.mu.Unlock()
	choice, configErr := a.selectedNAT(nil)
	a.refreshNATChoice(ctx, ds, choice, configErr)
}

func (a *Agent) refreshNATForDesired(ctx context.Context, ds *agentproto.DesiredState, cfg natconfig.Config, configErr error) {
	a.refreshNATChoice(ctx, ds, natChoice(cfg, "local", configErr), configErr)
}

func (a *Agent) refreshNATChoice(ctx context.Context, ds *agentproto.DesiredState, choice *agentproto.NATStatus, configErr error) {
	if err := natMutationAllowed(); err != nil {
		if configErr == nil {
			choice.Status, choice.Error = "pending", boundedNATError(err)
		}
		a.setNATStatus(choice)
		return
	}
	cfg := natconfig.Config{Schema: 1}
	if choice.Config != nil {
		cfg = *choice.Config
	}
	if ds == nil || a.State.ApplyError != "" {
		// A global failure may coexist with independently verified cores.
		// Without a complete current snapshot, retain that partial table and
		// retry application; do not turn a failed sibling into an outage.
		if a.natReady && configErr == nil && len(cfg.Mappings) > 0 && a.natAppliedKey == natChoiceKey(choice) {
			choice.Status, choice.Error = "pending", "等待节点配置完整确认"
			a.setNATStatus(choice)
			return
		}
		a.natReady = false
		err := a.clearNAT(ctx)
		if configErr != nil {
			err = errors.Join(err, configErr)
		}
		if a.State.NATVersion > 0 {
			if a.State.ApplyError == "" && a.State.AppliedRevision > 0 {
				a.State.ApplyError = "NAT 规则等待重新读取并确认节点配置"
			}
			err = errors.Join(err, a.invalidateNATApplications())
		}
		if err != nil {
			a.State.ApplyError = "NAT 端口适配失败: " + err.Error()
			a.Logger.Error("refresh NAT local authority", "err", err)
			choice.Status, choice.Error = "error", boundedNATError(err)
		} else if len(cfg.Mappings) == 0 {
			choice.Status, choice.Error = "disabled", ""
		} else {
			choice.Status, choice.Error = "pending", "等待重新读取并确认节点配置"
		}
		a.setNATStatus(choice)
		return
	}
	var nodes []agentproto.NodeSpec
	var forwards []agentproto.ForwardSpec
	nodes, forwards = ds.Nodes, ds.Forwards
	if err := a.applyNATChoice(ctx, choice, configErr, nodes, forwards); err != nil {
		a.State.ApplyError = err.Error()
		if err := a.invalidateNATApplications(); err != nil {
			a.Logger.Error("persist NAT failure receipts", "err", err)
		}
	}
}

// The controller and core keep their public/logical port. Only incoming IPv4
// packets are translated, before the existing admission and accounting hooks.
func (a *Agent) syncNAT(ctx context.Context, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) error {
	choice, err := a.selectedNAT(nil)
	return a.applyNATChoice(ctx, choice, err, nodes, forwards)
}

func (a *Agent) syncNATForDesired(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) error {
	if err := a.rememberNATIntent(ds); err != nil {
		return err
	}
	choice, err := a.selectedNAT(ds)
	return a.applyNATChoice(ctx, choice, err, nodes, forwards)
}

func (a *Agent) syncNATConfig(ctx context.Context, cfg natconfig.Config, err error, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) error {
	return a.applyNATChoice(ctx, natChoice(cfg, "local", err), err, nodes, forwards)
}

func (a *Agent) applyNATChoice(ctx context.Context, choice *agentproto.NATStatus, err error, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) error {
	if denied := natMutationAllowed(); denied != nil {
		if err == nil {
			choice.Status, choice.Error = "pending", boundedNATError(denied)
		}
		a.setNATStatus(choice)
		return denied
	}
	cfg := natconfig.Config{Schema: 1}
	if choice.Config != nil {
		cfg = *choice.Config
	}
	if err == nil {
		var rules string
		rules, err = nft.NATRules(cfg, nodes, forwards)
		if err == nil && rules != "" {
			err = checkNATListeners(rules, cfg)
		}
	}
	if err == nil && choice.Source == "local" && len(cfg.Mappings) > 0 && a.State.NATVersion == 0 {
		// Persist the executable requirement before changing the host, so a
		// downgrade cannot silently abandon configured NAT access.
		a.State.NATVersion = 1
		err = a.State.Save(a.StateDir)
	}
	if err == nil {
		// Repeat the authorization at the final mutation boundary.
		if denied := natMutationAllowed(); denied != nil {
			choice.Status, choice.Error = "pending", boundedNATError(denied)
			a.setNATStatus(choice)
			return denied
		}
		err = a.NFT.EnsureNAT(ctx, cfg, nodes, forwards)
	}
	if err != nil {
		a.natReady = false
		// Never retain a stale translation when local authority is malformed,
		// conflicting or no longer readable. Other nft tables are untouched.
		err = errors.Join(fmt.Errorf("NAT 端口适配失败: %w", err), a.clearNAT(ctx))
		choice.Status, choice.Error = "error", boundedNATError(err)
		a.setNATStatus(choice)
		return err
	}
	a.natAppliedKey, a.natReady = natChoiceKey(choice), true
	choice.Status, choice.Error = "applied", ""
	if !choice.Configured {
		choice.Status = "disabled"
	}
	a.setNATStatus(choice)
	return nil
}

func natMutationAllowed() error {
	p, err := secureupdate.LoadPolicy()
	if err != nil {
		return err
	}
	if p.PauseConfig {
		return errors.New("本机安全策略已暂停配置变更")
	}
	return secureupdate.Allow("agent.configure")
}

// rememberNATIntent is memory-only apart from the executable requirement. It
// never writes or removes the root-owned local override and stores no paths or
// commands supplied by the controller.
func (a *Agent) rememberNATIntent(ds *agentproto.DesiredState) error {
	if ds == nil {
		return errors.New("NAT 配置缺少节点配置上下文")
	}
	if err := natMutationAllowed(); err != nil {
		return err
	}
	var cfg *natconfig.Config
	if ds.NAT != nil {
		if ds.NATVersion != agentproto.NATVersion {
			return errors.New("NAT 面板配置版本无效")
		}
		if err := ds.NAT.Validate(); err != nil {
			return err
		}
		c := ds.NAT.Normalized()
		cfg = &c
		if a.State.NATVersion < agentproto.NATVersion {
			a.State.NATVersion = agentproto.NATVersion
			if err := a.State.Save(a.StateDir); err != nil {
				return err
			}
		}
	}
	a.mu.Lock()
	a.natController = cfg
	a.mu.Unlock()
	return nil
}

func natChoice(cfg natconfig.Config, source string, err error) *agentproto.NATStatus {
	s := &agentproto.NATStatus{Version: agentproto.NATVersion, Source: source, Status: "pending"}
	if err != nil {
		s.Status, s.Error = "error", boundedNATError(err)
		return s
	}
	if source != "none" {
		c := cfg.Normalized()
		s.Config, s.Configured = &c, len(c.Mappings) > 0
	}
	return s
}

// selectNATAuthority is pure so priority, explicit local opt-out, and corrupt
// local authority can be tested without touching host configuration files.
func selectNATAuthority(local natconfig.Config, present bool, localErr error, controller *natconfig.Config) (*agentproto.NATStatus, error) {
	if present || localErr != nil {
		if localErr == nil {
			localErr = local.Validate()
		}
		return natChoice(local, "local", localErr), localErr
	}
	if controller != nil {
		err := controller.Validate()
		return natChoice(*controller, "controller", err), err
	}
	return natChoice(natconfig.Config{Schema: 1}, "none", nil), nil
}

func (a *Agent) selectedNAT(ds *agentproto.DesiredState) (*agentproto.NATStatus, error) {
	a.mu.Lock()
	controller := a.natController
	a.mu.Unlock()
	if ds != nil {
		controller = ds.NAT
	}
	local, present, err := natconfig.LoadLocal()
	return selectNATAuthority(local, present, err, controller)
}

func boundedNATError(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToValidUTF8(err.Error(), "�")
	if len(s) <= 512 {
		return s
	}
	s = s[:512]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func natChoiceKey(s *agentproto.NATStatus) string {
	b, _ := json.Marshal(struct {
		Source string            `json:"source"`
		Config *natconfig.Config `json:"config"`
	}{s.Source, s.Config})
	return string(b)
}

func cloneNATStatus(s *agentproto.NATStatus) *agentproto.NATStatus {
	if s == nil {
		return nil
	}
	c := *s
	if s.Config != nil {
		cfg := s.Config.Normalized()
		c.Config = &cfg
	}
	return &c
}

func (a *Agent) setNATStatus(s *agentproto.NATStatus) {
	a.mu.Lock()
	a.natStatus = cloneNATStatus(s)
	a.mu.Unlock()
}

// Reporting is read-only and remains available while configuration is paused.
// A different authority/configuration cannot inherit an earlier success.
func (a *Agent) natDiagnostics() *agentproto.NATStatus {
	choice, err := a.selectedNAT(nil)
	return a.natDiagnosticsChoice(choice, err, natMutationAllowed())
}

func (a *Agent) natDiagnosticsChoice(choice *agentproto.NATStatus, err, denied error) *agentproto.NATStatus {
	if err != nil {
		return choice
	}
	a.mu.Lock()
	cached := cloneNATStatus(a.natStatus)
	ds, applying := a.desired, a.applying
	a.mu.Unlock()
	if denied != nil {
		choice.Status, choice.Error = "pending", boundedNATError(denied)
		return choice
	}
	if cached == nil || natChoiceKey(cached) != natChoiceKey(choice) {
		return choice
	}
	if cached.Status == "applied" && (!a.natReady || applying || ds == nil || a.State.ApplyError != "" || ds.Revision != a.State.AppliedRevision || ds.Hash != a.State.AppliedHash) {
		cached.Status, cached.Error = "pending", "等待节点配置完整确认"
	}
	return cached
}

func (a *Agent) clearNAT(ctx context.Context) error {
	return a.NFT.EnsureNAT(ctx, natconfig.Config{Schema: 1}, nil, nil)
}

// The configured source port is not a node listener: redirecting an occupied
// port would steal another local service. The fixed proc paths contain no
// controller input. Established TCP origin sockets are deliberately excluded.
func checkNATListeners(rules string, cfg natconfig.Config) error {
	if runtime.GOOS != "linux" {
		return errors.New("NAT 端口适配仅支持 Linux")
	}
	ports, err := cfg.Expand()
	if err != nil {
		return err
	}
	for _, network := range []string{"tcp", "udp"} {
		for _, family := range []string{"", "6"} {
			raw, err := os.ReadFile("/proc/net/" + network + family)
			if os.IsNotExist(err) && family == "6" {
				continue
			}
			if err != nil {
				return errors.New("无法检查 NAT 本机端口占用")
			}
			listeners, err := natListenerPorts(raw, network)
			if err != nil {
				return err
			}
			for _, p := range ports {
				// NATRules includes only active node mappings; an unused local
				// mapping must never interfere with an unrelated local service.
				if p.Network == network && p.LocalPort != p.PublicPort && listeners[p.LocalPort] && strings.Contains(rules, fmt.Sprintf("%s dport %d ", network, p.LocalPort)) {
					return fmt.Errorf("NAT 本机 %s 端口 %d 已有监听服务", network, p.LocalPort)
				}
			}
		}
	}
	return nil
}

func natListenerPorts(raw []byte, network string) (map[int]bool, error) {
	ports := map[int]bool{}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "local_address") {
		return nil, errors.New("无法解析 NAT 本机端口状态")
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, errors.New("无法解析 NAT 本机端口状态")
		}
		if network == "tcp" && fields[3] != "0A" {
			continue
		}
		_, port, ok := strings.Cut(fields[1], ":")
		value, err := strconv.ParseUint(port, 16, 16)
		if !ok || err != nil {
			return nil, errors.New("无法解析 NAT 本机端口状态")
		}
		ports[int(value)] = true
	}
	return ports, nil
}

// Local NAT failures need a new receipt sequence even when the remote revision
// did not change; otherwise its last success could remain in subscriptions.
func (a *Agent) invalidateNATApplications() error {
	if len(a.State.NodeApplications) == 0 {
		return nil
	}
	changed := false
	for _, application := range a.State.NodeApplications {
		changed = changed || application.Status != "failed"
	}
	if !changed {
		return nil
	}
	a.State.NodeApplicationSequence++
	for i := range a.State.NodeApplications {
		a.State.NodeApplications[i].Status = "failed"
	}
	if err := a.State.Save(a.StateDir); err != nil {
		a.State.NodeApplicationRevision, a.State.NodeApplicationHash = 0, ""
		a.State.NodeApplications = nil
		return err
	}
	return nil
}
