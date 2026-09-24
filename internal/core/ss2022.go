package core

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
	"errors"
)

func CompileResourceSS2022(resource agentproto.ResourceIdentity, cfg networkconfig.SS2022, credentials networkconfig.SOCKS5Credentials, transport networkconfig.SOCKS5, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	var empty SOCKS5Compilation
	if err := cfg.Validate(); err != nil {
		return empty, err
	}
	if err := credentials.ValidateSS2022(cfg.Method); err != nil {
		return empty, err
	}
	if transport.Purpose != "ss2022" || transport.ServerPort != cfg.ServerPort || !transport.UDP {
		return empty, errors.New("SS-2022 端点与配置不一致")
	}
	compiled, err := CompileResourceSOCKS5(resource, transport, networkconfig.SOCKS5Credentials{}, outer)
	if err != nil {
		return empty, err
	}
	compiled.Outbound["type"] = "shadowsocks"
	compiled.Outbound["method"] = cfg.Method
	compiled.Outbound["password"] = credentials.Password
	delete(compiled.Outbound, "version")
	delete(compiled.Outbound, "udp_over_tcp")
	return compiled, nil
}
