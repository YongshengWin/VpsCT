//go:build !linux

package netinventory

import (
	"context"
	"errors"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/networkconfig"
)

func LookupBootstrap(context.Context, int64, string, string, networkconfig.ResolvedDirect) (agentnet.DNSResult, error) {
	return agentnet.DNSResult{}, errors.New("绑定启动 DNS 仅支持 Linux agent")
}

func LookupBootstrapWithGrants(context.Context, int64, int64, string, string, networkconfig.ResolvedDirect, []networkconfig.TransportGrant) (agentnet.DNSResult, error) {
	return agentnet.DNSResult{}, errors.New("绑定启动 DNS 仅支持 Linux agent")
}

func LookupForwardBootstrap(context.Context, int64, string, string, networkconfig.ResolvedDirect) (agentnet.DNSResult, error) {
	return agentnet.DNSResult{}, errors.New("固定转发解析需要 Linux")
}
