package agentproto

import (
	"errors"
	"unicode/utf8"

	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
)

const NATVersion = networkconfig.NATVersion

// NATStatus reports the currently selected authority separately from the
// sticky local executable requirement. Applied means host reconciliation; it
// does not attest to a provider's public endpoint or TCP/UDP reachability.
type NATStatus struct {
	Version    int               `json:"version"`
	Source     string            `json:"source"` // none | local | controller
	Configured bool              `json:"configured"`
	Status     string            `json:"status"` // disabled | pending | applied | error
	Config     *natconfig.Config `json:"config,omitempty"`
	Error      string            `json:"error,omitempty"`
}

func ValidateNAT(s *NATStatus) error {
	if s == nil {
		return nil
	}
	if s.Version != NATVersion || len(s.Error) > 512 || !utf8.ValidString(s.Error) {
		return errors.New("NAT 状态版本或错误文本无效")
	}
	switch s.Source {
	case "none":
		if s.Config != nil || s.Configured {
			return errors.New("未配置的 NAT 状态不能包含配置")
		}
	case "local", "controller":
	default:
		return errors.New("NAT 配置来源无效")
	}
	if s.Config != nil {
		if err := s.Config.Validate(); err != nil {
			return err
		}
		if s.Configured != (len(s.Config.Mappings) > 0) {
			return errors.New("NAT 启用标记与配置不一致")
		}
	} else if s.Configured {
		return errors.New("已配置的 NAT 状态缺少有效快照")
	}
	switch s.Status {
	case "disabled":
		if s.Configured || s.Error != "" {
			return errors.New("已关闭的 NAT 状态无效")
		}
	case "applied":
		if !s.Configured || s.Error != "" {
			return errors.New("NAT 应用状态无效")
		}
	case "error":
		if s.Error == "" {
			return errors.New("NAT 失败状态缺少原因")
		}
	case "pending":
	default:
		return errors.New("NAT 应用状态无效")
	}
	if s.Config == nil && s.Source != "none" && s.Status != "error" {
		return errors.New("NAT 配置来源缺少当前快照")
	}
	return nil
}
