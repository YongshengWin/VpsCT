package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

type ipv6DeployPreview struct {
	store.IPv6DeploymentPreview
	Port      int    `json:"port"`
	Confirmed string `json:"confirmation,omitempty"`
}

type ipv6EndpointStatus struct {
	Host             string    `json:"host"`
	Address          string    `json:"address,omitempty"`
	DNSReady         bool      `json:"dns_ready"`
	Matched          bool      `json:"matched"`
	CheckedAt        time.Time `json:"checked_at"`
	Message          string    `json:"message"`
	dnsError         error
	inventoryChecked bool
}

func (a *API) getServerIPv6Endpoint(w http.ResponseWriter, r *http.Request) error {
	host := r.URL.Query().Get("host")
	return a.inspectServerIPv6Endpoint(w, r, host)
}

func (a *API) checkServerIPv6Endpoint(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Host string `json:"host"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return a.inspectServerIPv6Endpoint(w, r, in.Host)
}

func (a *API) inspectServerIPv6Endpoint(w http.ResponseWriter, r *http.Request, rawHost string) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
	defer cancel()
	server, err := a.Store.GetServer(ctx, id)
	if err != nil {
		return ipv6EndpointCheckError(err)
	}
	host, err := networkconfig.NormalizeIPv6Host(rawHost)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if server.IsNAT {
		return httpx.BadRequest("IPv6 接入地址检查需要独立公网服务器")
	}
	out, err := a.ipv6EndpointStatus(ctx, id, host)
	if err != nil {
		return ipv6EndpointCheckError(err)
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) ipv6EndpointStatus(ctx context.Context, serverID int64, host string) (ipv6EndpointStatus, error) {
	address, dnsErr := a.resolveIPv6Endpoint(ctx, host)
	out := ipv6EndpointStatus{Host: host, Address: address, DNSReady: dnsErr == nil, CheckedAt: a.Store.Now(), dnsError: dnsErr}
	if dnsErr != nil {
		out.Message = dnsErr.Error()
		return out, nil
	}
	out.Message = "等待 agent 上报当前网卡清单"
	if serverID == 0 {
		return out, nil
	}
	inventory, err := a.Store.Network(ctx, serverID)
	if err != nil {
		return out, err
	}
	if inventory.Snapshot == nil || inventory.Snapshot.Status != "ok" || inventory.ReceivedAt == nil || a.Store.Now().Sub(*inventory.ReceivedAt) > 2*time.Minute {
		return out, nil
	}
	out.inventoryChecked = true
	out.Message = "访问地址未匹配可用的本机公网 IPv6"
	for _, iface := range inventory.Snapshot.Interfaces {
		policy := networkconfig.Node{IngressFamily: "ipv6", ListenMode: "address", ListenAddress: address,
			ListenInterfaceID: iface.ID, AdvertiseMode: "inherit_ipv6", OnUnavailable: "block"}
		if netinventory.ValidateBindingSelection(policy, nil, inventory.Snapshot, false) == nil {
			out.Matched = true
			out.Message = "访问地址与本机公网 IPv6 一致"
			break
		}
	}
	return out, nil
}

func (a *API) validateServerIPv6Change(ctx context.Context, server domain.Server, previousHost string) error {
	if server.IsNAT || server.PublicHostIPv6 == "" || server.PublicHostIPv6 == previousHost {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	out, err := a.ipv6EndpointStatus(ctx, server.ID, server.PublicHostIPv6)
	if err != nil {
		return ipv6EndpointCheckError(err)
	}
	if errors.Is(out.dnsError, errIPv6DNSTemporary) {
		return httpx.E(http.StatusServiceUnavailable, "ipv6_check_unavailable", "IPv6 DNS 检查暂不可用，请重试")
	}
	if !out.DNSReady || (out.inventoryChecked && !out.Matched) {
		return httpx.E(http.StatusBadRequest, "ipv6_endpoint_invalid", out.Message)
	}
	return nil
}

func ipv6EndpointCheckError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return httpx.E(http.StatusGatewayTimeout, "ipv6_check_timeout", "IPv6 地址检查超时，请重试")
	}
	return networkOperationError(err)
}

func (a *API) previewIPv6Node(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	server, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in deployInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, preview, err := a.checkIPv6Deployment(r.Context(), server, in)
	if err != nil {
		return err
	}
	httpx.OK(w, preview)
	return nil
}

func ipv6DeploymentHash(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (a *API) checkIPv6Deployment(ctx context.Context, server domain.Server, in deployInput) (domain.Node, ipv6DeployPreview, error) {
	var node domain.Node
	var preview ipv6DeployPreview
	if in.IngressFamily != "ipv6" || in.Network == nil || in.Network.IngressFamily != "ipv6" || in.Network.EgressProfileID != 0 || in.Network.Ingress != nil || server.IsNAT {
		return node, preview, httpx.BadRequest("仅 IPv6 部署需要独立公网和仅 IPv6 监听；出口及来源限制可在部署后单独设置")
	}
	if (in.VLESS != nil && in.VLESS.Security == "tls") || (in.Trojan != nil && (in.Trojan.Transport == "ws" || in.Trojan.Transport == "grpc")) {
		return node, preview, httpx.BadRequest("该 TLS 传输组合目前只支持普通接入")
	}
	used, err := a.Store.UsedListenPorts(ctx, server.ID)
	if err != nil {
		return node, preview, err
	}
	port := in.Port
	if port == 0 {
		port, err = provision.AllocateNodePort(used, nil, in.Protocol, nil)
		if err != nil {
			return node, preview, httpx.BadRequest(err.Error())
		}
	} else if used[port] {
		return node, preview, httpx.Conflict(fmt.Sprintf("端口 %d 已被占用", port))
	}
	options := in.options(port)
	node, err = provision.NewNode(server, "", options)
	if err != nil {
		return node, preview, httpx.BadRequest(err.Error())
	}
	if node.Core != domain.CoreSingBox || node.Protocol == domain.ProtocolWireGuard {
		return node, preview, httpx.BadRequest("该协议尚不支持仅 IPv6 部署")
	}
	preview.Port = port
	preview.IPv6DeploymentPreview, err = a.Store.PreviewIPv6Deployment(ctx, node)
	if err != nil {
		return node, preview, networkOperationError(err)
	}
	if !preview.Readiness.Ready {
		return node, preview, nil
	}
	dnsErr := a.verifyIPv6Endpoint(ctx, preview.Host, preview.ListenAddress)
	preview.Readiness.Checks = append(preview.Readiness.Checks, store.NetworkReadinessCheck{
		Code: "ipv6_dns", Ready: dnsErr == nil, Message: "访问地址与服务器的公网 IPv6 一致",
	})
	if dnsErr != nil {
		preview.Readiness.Ready = false
		preview.Readiness.Checks[len(preview.Readiness.Checks)-1].Message = dnsErr.Error()
		return node, preview, nil
	}
	in.Port, in.ExpectedIPv6, in.OperationID = port, "", ""
	preview.Confirmed = ipv6DeploymentHash([]any{server.ID, server.CertMode, in, preview.Host, preview.ListenAddress, preview.ListenInterfaceID, preview.Readiness.PinnedCoreVersion})
	if node.Network.ListenMode == "public_ipv6" {
		node.Network.ListenInterfaceID = preview.ListenInterfaceID
	}
	return node, preview, nil
}

func (a *API) deployIPv6Node(w http.ResponseWriter, r *http.Request, server domain.Server, in deployInput) error {
	if !networkconfig.ValidIdentity(in.OperationID) || len(in.ExpectedIPv6) != 64 || in.Port <= 0 {
		return httpx.BadRequest("请先预览仅 IPv6 部署，并提交预览确认值、端口和操作 ID")
	}
	ctx := r.Context()
	owner := userFrom(ctx).ID
	fingerprint := ipv6DeploymentHash(in)
	node, exists, err := a.Store.PriorIPv6Deployment(ctx, server.ID, owner, in.OperationID, fingerprint)
	if err != nil {
		return networkOperationError(err)
	}
	if !exists {
		node, preview, err := a.checkIPv6Deployment(ctx, server, in)
		if err != nil {
			return err
		}
		if !preview.Readiness.Ready || preview.Confirmed != in.ExpectedIPv6 {
			return httpx.Conflict("IPv6 部署条件已变化，请重新预览")
		}
		node.OwnerUserID, node.Tags = owner, in.Tags
		audit := a.auditIdentity(r)
		audit.Action, audit.Target = "node.deploy", node.Name
		audit.Detail, _ = json.Marshal(map[string]any{"port": node.ListenPort, "ingress_family": "ipv6"})
		if err = a.Store.CreateIPv6Deployment(ctx, &node, in.OperationID, fingerprint, preview.IPv6DeploymentPreview, audit); err != nil {
			return networkOperationError(err)
		}
		return a.finishIPv6Deployment(w, r, node)
	}
	return a.finishIPv6Deployment(w, r, node)
}

func (a *API) finishIPv6Deployment(w http.ResponseWriter, r *http.Request, node domain.Node) error {
	w.Header().Set("Location", fmt.Sprintf("/api/v1/nodes/%d", node.ID))
	if _, _, err := a.Desired.Publish(r.Context(), *node.ServerID); err != nil {
		a.Logger.Warn("IPv6 deployment publication pending", "node_id", node.ID, "err", err)
		httpx.JSON(w, http.StatusAccepted, node)
		return nil
	}
	httpx.JSON(w, http.StatusCreated, node)
	return nil
}

var errIPv6DNSTemporary = errors.New("IPv6 DNS 检查暂不可用")

// RefreshIPv6Endpoints validates the published hostname against the exact
// listener the agent applied. DNS never nominates a listener outside inventory.
func (a *API) RefreshIPv6Endpoints(ctx context.Context) error {
	checks, err := a.Store.IPv6EndpointChecksDue(ctx, 4)
	if err != nil {
		return err
	}
	var group sync.WaitGroup
	failures := make(chan error, len(checks))
	for _, check := range checks {
		group.Go(func() {
			state, message := "ok", ""
			if err := a.verifyIPv6Endpoint(ctx, check.Host, check.ListenIPv6); err != nil {
				state, message = "invalid", err.Error()
				if errors.Is(err, errIPv6DNSTemporary) {
					state = "temporary"
				}
			}
			if err := a.Store.RecordIPv6EndpointCheck(ctx, check, state, message); err != nil {
				failures <- err
			}
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	return nil
}

func (a *API) verifyIPv6Endpoint(ctx context.Context, host, listen string) error {
	address, err := a.resolveIPv6Endpoint(ctx, host)
	if address != "" && address != listen {
		return errors.New("IPv6 访问地址与本机监听地址不一致")
	}
	return err
}

// The resolver only receives normalized hostnames. Literal addresses skip DNS.
func (a *API) resolveIPv6Endpoint(ctx context.Context, host string) (string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String(), nil
	}
	if !a.ipv6DNSGate.Acquire() {
		return "", errIPv6DNSTemporary
	}
	defer a.ipv6DNSGate.Release()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lookup := net.DefaultResolver.LookupNetIP
	if a.ipv6Lookup != nil {
		lookup = a.ipv6Lookup
	}
	var dnsErr *net.DNSError
	aaaa, err := lookup(ctx, "ip6", host+".")
	if err != nil {
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return "", errors.New("IPv6 访问域名没有可用的 AAAA 解析")
		}
		return "", errIPv6DNSTemporary
	}
	if len(aaaa) != 1 || !networkconfig.IsPublicIPv6(aaaa[0].String()) {
		return "", errors.New("访问域名需要一个可用的公网 AAAA 记录")
	}
	ipv4, err := lookup(ctx, "ip4", host+".")
	if len(ipv4) != 0 {
		return "", errors.New("仅 IPv6 访问域名不能包含 A 记录")
	}
	if err != nil && (!errors.As(err, &dnsErr) || !dnsErr.IsNotFound) {
		return aaaa[0].String(), errIPv6DNSTemporary
	}
	return aaaa[0].String(), nil
}
