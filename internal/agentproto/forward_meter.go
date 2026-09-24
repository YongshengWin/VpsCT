package agentproto

import (
	"errors"
	"math"
	"strings"

	"ctlvps/internal/agentbudget"
)

// ForwardCounter has no node/share/listen-port identity. Its cumulative values
// include both client and target sockets, with receive/send kept independent.
type ForwardCounter struct {
	ForwardID int64  `json:"forward_id"`
	Epoch     string `json:"epoch"`
	Rx        int64  `json:"rx"`
	Tx        int64  `json:"tx"`
	RxPkts    int64  `json:"rx_pkts,omitempty"`
	TxPkts    int64  `json:"tx_pkts,omitempty"`
}

func (c ForwardCounter) ValidateValues() error {
	if _, err := (ResourceIdentity{Kind: "forward", ID: c.ForwardID}).Mark(); err != nil {
		return err
	}
	for _, value := range []int64{c.Rx, c.Tx, c.RxPkts, c.TxPkts} {
		if value < 0 || value > math.MaxInt64/4 {
			return errors.New("转发计数超出范围")
		}
	}
	return nil
}

func ValidateForwardCounters(counters []ForwardCounter) error {
	if len(counters) > 2*agentbudget.ActiveForwards {
		return errors.New("转发计量记录超出预算")
	}
	seen := map[int64]bool{}
	for _, c := range counters {
		if err := c.ValidateValues(); err != nil {
			return err
		}
		if seen[c.ForwardID] || c.Epoch == "" || len(c.Epoch) > 128 || strings.IndexFunc(c.Epoch, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return errors.New("转发计量身份或代次无效")
		}
		seen[c.ForwardID] = true
	}
	return nil
}

// ForwardMeterRule contains only persisted identity needed to retain/freeze a
// counter during retirement. It cannot configure a target or start a listener.
type ForwardMeterRule struct {
	ForwardID  int64 `json:"forward_id"`
	ListenPort int   `json:"listen_port"`
	Blocked    bool  `json:"blocked"`
	Retired    bool  `json:"retired"`
}
