//go:build linux

package netinventory

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
	"golang.org/x/sys/unix"
)

// LookupBootstrap opens a bounded DNS socket with the node's outer binding.
// Its distinct root-only mark charges DNS bytes without granting the pending
// proxy any business traffic. Only the child parses remote DNS wire data.
func LookupBootstrap(ctx context.Context, nodeID int64, host, family string, outer networkconfig.ResolvedDirect) (agentnet.DNSResult, error) {
	return LookupBootstrapWithGrants(ctx, nodeID, 0, host, family, outer, nil)
}

func LookupBootstrapWithGrants(ctx context.Context, nodeID, profileID int64, host, family string, outer networkconfig.ResolvedDirect, grants []networkconfig.TransportGrant) (agentnet.DNSResult, error) {
	mark, err := agentproto.BootstrapMark(nodeID)
	if err != nil {
		return agentnet.DNSResult{}, err
	}
	return lookupBootstrapMarked(ctx, nodeID, profileID, host, family, outer, grants, mark)
}
func LookupForwardBootstrap(ctx context.Context, id int64, host, family string, outer networkconfig.ResolvedDirect) (agentnet.DNSResult, error) {
	mark, err := agentproto.ForwardBootstrapMark(id)
	if err != nil {
		return agentnet.DNSResult{}, err
	}
	return lookupBootstrapMarked(ctx, 0, 0, host, family, outer, nil, mark)
}
func lookupBootstrapMarked(ctx context.Context, nodeID, profileID int64, host, family string, outer networkconfig.ResolvedDirect, grants []networkconfig.TransportGrant, mark uint32) (agentnet.DNSResult, error) {
	var empty agentnet.DNSResult
	if err := outer.Config.Validate(); err != nil {
		return empty, err
	}
	if (outer.Config.InterfaceID == "") != (outer.Interface == nil) || (outer.Interface != nil && (outer.Interface.ID != outer.Config.InterfaceID || outer.Interface.Index < 1 || outer.Interface.Name == "" || len(outer.Interface.Name) > 15 || strings.ContainsAny(outer.Interface.Name, "/:\x00\r\n\t "))) {
		return empty, errors.New("启动 DNS 外层接口尚未本机解析")
	}
	resolver := outer.Config.DNS
	ip, _ := networkconfig.HostAddress(resolver.Address)
	endpoint := networkconfig.ResolvedSOCKS5{Address: ip.String(), Port: resolver.Port}
	if networkconfig.NeedsTransportGrant(ip) {
		for _, network := range []string{"tcp", resolver.Transport} {
			if !networkconfig.AuthorizeTransport(grants, nodeID, profileID, "bootstrap_dns", network, ip, resolver.Port) {
				return empty, errors.New("私网启动 DNS 传输未被本机精确授权")
			}
		}
	} else if err := endpoint.Validate(); err != nil {
		return empty, err
	}
	transport := resolver.Transport
	for attempt := 0; attempt < 2; attempt++ {
		var local net.Addr
		source := outer.Config.SourceIPv6
		if ip.Is4() {
			source = outer.Config.SourceIPv4
		}
		if source != nil {
			if transport == "tcp" {
				local = &net.TCPAddr{IP: net.ParseIP(source.Address)}
			} else {
				local = &net.UDPAddr{IP: net.ParseIP(source.Address)}
			}
		}
		dialer := net.Dialer{Timeout: 3 * time.Second, LocalAddr: local, Control: func(_, _ string, raw syscall.RawConn) error {
			var socketErr error
			if err := raw.Control(func(fd uintptr) {
				socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
				if socketErr == nil && outer.Interface != nil {
					socketErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, outer.Interface.Name)
				}
			}); err != nil {
				return err
			}
			return socketErr
		}}
		conn, err := dialer.DialContext(ctx, transport, net.JoinHostPort(ip.String(), strconv.Itoa(resolver.Port)))
		if err != nil {
			return empty, errors.New("指定网卡的启动 DNS 连接失败")
		}
		result, err := agentnet.QueryDNS(ctx, conn, host, family, transport)
		conn.Close()
		if err != nil {
			return empty, err
		}
		if !result.Truncated {
			return result, nil
		}
		transport = "tcp" // same literal resolver, interface, source and mark
	}
	return empty, errors.New("启动 DNS 截断重试失败")
}
