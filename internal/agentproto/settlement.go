package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"
)

const SettlementBatchSize = 128

// MeterSettlement is an immutable final snapshot. Its content-addressed ID
// binds the timestamp, generations and readings, making retries unambiguous.
type MeterSettlement struct {
	ID       string        `json:"id"`
	TS       time.Time     `json:"ts"`
	Counters []PortCounter `json:"counters"`
}

func (s MeterSettlement) Digest() string {
	s.ID = ""
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s MeterSettlement) Validate() error {
	if s.TS.IsZero() || len(s.Counters) == 0 || len(s.Counters) > SettlementBatchSize || len(s.ID) != 64 || s.ID != s.Digest() {
		return errors.New("invalid final meter batch")
	}
	seen := map[int64]bool{}
	for _, c := range s.Counters {
		if c.NodeID <= 0 || seen[c.NodeID] || !c.FromZero || (c.Source != "nft-node-v1" && c.Source != "systemd-v1") || len(c.Epoch) == 0 || len(c.Epoch) > 128 || c.Rx < 0 || c.Tx < 0 || c.Rx > math.MaxInt64/4 || c.Tx > math.MaxInt64/4 {
			return errors.New("invalid final meter reading")
		}
		seen[c.NodeID] = true
	}
	return nil
}
