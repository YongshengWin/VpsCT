package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ValidateDesired is repeated at the final privileged execution boundary.
func ValidateDesired(d *DesiredState, serverID, lastRevision int64, lastHash string) error {
	if d == nil || d.ServerID != serverID || d.Revision < 1 || d.Revision < lastRevision || (d.Revision == lastRevision && lastHash != "" && d.Hash != lastHash) {
		return errors.New("配置身份或代次无效")
	}
	if len(d.Nodes) > 2048 || len(d.Hash) != 64 || d.Hash != ContentHash(d) {
		return errors.New("配置大小或摘要无效")
	}
	if d.Tuning.MemoryMaxMB < 0 || d.Tuning.MemoryMaxMB > 65536 || d.Tuning.GoMemLimitMB < 0 || d.Tuning.GoMemLimitMB > 65536 || d.Tuning.LimitNOFILE < 0 || d.Tuning.LimitNOFILE > 1048576 || d.Tuning.RestartSec < 0 || d.Tuning.RestartSec > 300 {
		return errors.New("调优参数超出本机允许范围")
	}
	if d.Connlog.BatchSize < 0 || d.Connlog.BatchSize > 10000 || d.Connlog.MaxBufferMB < 0 || d.Connlog.MaxBufferMB > 64 || d.Connlog.FlushSec < 0 || d.Connlog.FlushSec > 3600 {
		return errors.New("日志参数越界")
	}
	ids := map[int64]bool{}
	ports := map[int]bool{}
	for _, n := range d.Nodes {
		if n.NodeID < 1 || ids[n.NodeID] || n.ListenPort < 1 || n.ListenPort > 65535 || ports[n.ListenPort] {
			return errors.New("节点标识或端口无效")
		}
		ids[n.NodeID] = true
		ports[n.ListenPort] = true
		if n.Core != "singbox" && n.Core != "snell" {
			return errors.New("不支持的代理内核")
		}
		if n.Core == "snell" && n.Protocol != "snell" {
			return errors.New("协议与内核不一致")
		}
		if err := ValidateParams(n.Params, 0); err != nil {
			return err
		}
		if n.Cert != nil && n.Cert.Mode != "self_signed" && n.Cert.Mode != "acme" && n.Cert.Mode != "external" {
			return errors.New("证书模式无效")
		}
	}
	return nil
}

func ValidateParams(v any, depth int) error {
	if depth > 16 {
		return errors.New("参数嵌套过深")
	}
	switch x := v.(type) {
	case string:
		if len(x) > 16384 || strings.ContainsAny(x, "\x00\r\n") {
			return errors.New("参数包含非法字符或过长")
		}
	case map[string]any:
		if len(x) > 128 {
			return errors.New("参数过多")
		}
		for k, v := range x {
			if len(k) > 128 || strings.ContainsAny(k, "\x00\r\n") {
				return errors.New("参数名无效")
			}
			if err := ValidateParams(v, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(x) > 2048 {
			return errors.New("参数集合过大")
		}
		for _, v := range x {
			if err := ValidateParams(v, depth+1); err != nil {
				return err
			}
		}
	case nil, bool, int, int64, float64, json.Number:
	default:
		return errors.New("不支持的参数类型")
	}
	return nil
}

// ContentHash binds every desired-state field other than versioning metadata.
func ContentHash(d *DesiredState) string {
	cp := *d
	cp.Revision = 0
	cp.Hash = ""
	cp.GeneratedAt = time.Time{}
	b, _ := json.Marshal(cp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
