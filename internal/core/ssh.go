package core

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// CompileSSH uses the common marked outer path and per-node TCP business DNS.
// Authentication and host keys are never copied into public node parameters.
func CompileSSH(nodeID int64, cfg networkconfig.SSH, secret networkconfig.SOCKS5Credentials, transport networkconfig.SOCKS5, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	return CompileResourceSSH(agentproto.ResourceIdentity{Kind: "node", ID: nodeID}, cfg, secret, transport, outer)
}

func CompileResourceSSH(resource agentproto.ResourceIdentity, cfg networkconfig.SSH, secret networkconfig.SOCKS5Credentials, transport networkconfig.SOCKS5, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	var out SOCKS5Compilation
	if err := cfg.Validate(); err != nil {
		return out, err
	}
	if err := secret.ValidateSSH(cfg); err != nil {
		return out, err
	}
	out, err := CompileResourceSOCKS5(resource, transport, networkconfig.SOCKS5Credentials{}, outer)
	if err != nil {
		return out, err
	}
	delete(out.Outbound, "version")
	delete(out.Outbound, "network")
	delete(out.Outbound, "udp_over_tcp")
	for k, v := range secret.SSHClient(cfg).SingBox() {
		out.Outbound[k] = v
	}
	base, _ := resource.Tag()
	tag := base + "-ssh"
	out.Outbound["tag"] = tag
	out.DNS["detour"] = tag
	return out, nil
}
