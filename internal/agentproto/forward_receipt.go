package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"ctlvps/internal/networkconfig"
)

// ForwardReceipt is frozen only after the entire desired application succeeds:
// replaced listeners have closed and retired counters no longer have writers.
// The controller commits accounting and port release together before ACK.
type ForwardReceipt struct {
	ID       string           `json:"id"`
	Revision int64            `json:"revision"`
	Hash     string           `json:"hash"`
	TS       time.Time        `json:"ts"`
	Counters []ForwardCounter `json:"counters"`
}

func (r ForwardReceipt) Validate() error {
	hash, err := hex.DecodeString(r.Hash)
	if !networkconfig.ValidIdentity(r.ID) || r.Revision < 1 || r.Revision > 1<<53-1 || err != nil || len(hash) != 32 || r.TS.IsZero() {
		return errors.New("固定转发应用回执身份无效")
	}
	return ValidateForwardCounters(r.Counters)
}

func (r ForwardReceipt) Digest() string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
