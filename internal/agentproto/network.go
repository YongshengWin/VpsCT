package agentproto

import (
	"errors"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

const NetworkVersion = 1
const MaxNetworkInterfaces = 128
const MaxInterfaceAddresses = 32

// NetworkSnapshot is observational. Its counters never replace the legacy
// server quota source without a separately negotiated billing policy.
type NetworkSnapshot struct {
	Version     int                `json:"version"`
	CollectorID string             `json:"collector_id,omitempty"`
	BootID      string             `json:"boot_id"`
	Sequence    int64              `json:"sequence"`
	SampledAt   time.Time          `json:"sampled_at"`
	Status      string             `json:"status"` // ok | incomplete | error | unsupported
	Error       string             `json:"error,omitempty"`
	Interfaces  []NetworkInterface `json:"interfaces"`
}

type NetworkInterface struct {
	ID          string   `json:"id"`
	Generation  string   `json:"generation"`
	Name        string   `json:"name"`
	Index       int      `json:"index"`
	Kind        string   `json:"kind"`
	MAC         string   `json:"mac,omitempty"`
	MTU         int      `json:"mtu"`
	Up          bool     `json:"up"`
	Carrier     bool     `json:"carrier"`
	ParentIndex int      `json:"parent_index,omitempty"`
	MasterIndex int      `json:"master_index,omitempty"`
	Addresses   []string `json:"addresses"`
	// Older agents omit this field. Presence in Addresses alone is not proof
	// that IPv6 DAD/lifetime checks permit a new listener or source binding.
	UsableAddresses []string `json:"usable_addresses,omitempty"`
	DefaultIPv4     bool     `json:"default_ipv4"`
	DefaultIPv6     bool     `json:"default_ipv6"`
	CountersValid   bool     `json:"counters_valid"`
	Rx              int64    `json:"rx"`
	Tx              int64    `json:"tx"`
	RateValid       bool     `json:"rate_valid"`
	RxRate          int64    `json:"rx_rate"`
	TxRate          int64    `json:"tx_rate"`
}

var networkIdentity = regexp.MustCompile(`^[a-f0-9]{32}$`)

func ValidateNetwork(s *NetworkSnapshot) error {
	if s == nil {
		return nil
	}
	if s.Version != NetworkVersion || len(s.Interfaces) > MaxNetworkInterfaces || s.SampledAt.IsZero() || len(s.BootID) > 128 || len(s.Error) > 512 || s.Sequence < 0 || s.Sequence > 1<<53 {
		return errors.New("网卡快照格式或大小无效")
	}
	switch s.Status {
	case "ok", "incomplete":
		if !networkIdentity.MatchString(s.CollectorID) || s.BootID == "" || s.Sequence == 0 {
			return errors.New("网卡快照缺少代次")
		}
	case "error", "unsupported":
		if len(s.Interfaces) != 0 {
			return errors.New("失败快照不能包含网卡计数")
		}
	default:
		return errors.New("网卡采集状态无效")
	}
	ids, indexes := map[string]bool{}, map[int]bool{}
	for _, n := range s.Interfaces {
		if !networkIdentity.MatchString(n.ID) || !networkIdentity.MatchString(n.Generation) || ids[n.ID] || n.Index <= 0 || indexes[n.Index] || n.ParentIndex < 0 || n.MasterIndex < 0 || n.MTU < 0 || n.MTU > 1<<24 {
			return errors.New("网卡身份或参数无效")
		}
		ids[n.ID], indexes[n.Index] = true, true
		if n.Name == "" || len(n.Name) > 15 || strings.ContainsAny(n.Name, "/:\x00\r\n\t ") || n.Kind == "" || len(n.Kind) > 32 || strings.ContainsAny(n.Kind, "\x00\r\n") || len(n.Addresses) > MaxInterfaceAddresses || len(n.UsableAddresses) > MaxInterfaceAddresses {
			return errors.New("网卡名称或地址数量无效")
		}
		if n.MAC != "" {
			if _, err := net.ParseMAC(n.MAC); err != nil {
				return errors.New("网卡硬件地址无效")
			}
		}
		addresses := map[string]bool{}
		for _, raw := range n.Addresses {
			if _, err := netip.ParsePrefix(raw); err != nil {
				return errors.New("网卡地址无效")
			}
			addresses[raw] = true
		}
		for _, raw := range n.UsableAddresses {
			if !addresses[raw] {
				return errors.New("可用地址不在接口地址清单中")
			}
		}
		for _, count := range []int64{n.Rx, n.Tx, n.RxRate, n.TxRate} {
			if count < 0 || count > math.MaxInt64/4 {
				return errors.New("网卡计数越界")
			}
		}
		if (!n.CountersValid && (n.Rx != 0 || n.Tx != 0 || n.RateValid)) || (!n.RateValid && (n.RxRate != 0 || n.TxRate != 0)) {
			return errors.New("网卡计数状态无效")
		}
	}
	return nil
}
