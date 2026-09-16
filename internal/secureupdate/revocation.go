package secureupdate

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ctlvps/internal/safehttp"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

// ReleasePolicy is a regular TUF target, not an alternative signing protocol.
// Revocations are cumulative; accepting a new policy never forgets a denial.
type ReleasePolicy struct {
	Schema   int              `json:"schema"`
	Product  string           `json:"product"`
	Sequence int64            `json:"sequence"`
	Expires  time.Time        `json:"expires"`
	MinEpoch map[string]int64 `json:"min_epoch"`
	Revoked  []string         `json:"revoked"`
}

func (p ReleasePolicy) validate() error {
	if !p.Expires.After(time.Now()) {
		return errors.New("发布撤销策略已过期")
	}
	return p.validateStructure()
}
func (p ReleasePolicy) validateStructure() error {
	if p.Schema != 1 || p.Product != "VpsCT" || p.Sequence < 1 || p.Expires.IsZero() || len(p.Revoked) > 10000 {
		return errors.New("发布撤销策略无效或已过期")
	}
	for _, s := range p.Revoked {
		b, e := hex.DecodeString(s)
		if e != nil || len(b) != 32 || s != strings.ToLower(s) {
			return errors.New("撤销摘要无效")
		}
	}
	for _, epoch := range p.MinEpoch {
		if epoch < 1 {
			return errors.New("安全代次无效")
		}
	}
	return nil
}
func writeState(path string, data []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".security-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func readReleasePolicy(dir string) (ReleasePolicy, error) {
	var p ReleasePolicy
	b, e := os.ReadFile(filepath.Join(dir, "security-policy.json"))
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(b, &p)
	return p, e
}
func acceptReleasePolicy(dir string, next ReleasePolicy) error {
	if e := next.validate(); e != nil {
		return e
	}
	old, e := readReleasePolicy(dir)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if e == nil {
		if next.Sequence < old.Sequence {
			return errors.New("拒绝回退撤销策略")
		}
		if next.Sequence == old.Sequence {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(next)
			if !bytes.Equal(a, b) {
				return errors.New("同代次撤销策略内容变化")
			}
		}
		denied := map[string]bool{}
		for _, s := range next.Revoked {
			denied[s] = true
		}
		for _, s := range old.Revoked {
			if !denied[s] {
				return errors.New("新策略丢失历史撤销记录")
			}
		}
		for component, epoch := range old.MinEpoch {
			if next.MinEpoch[component] < epoch {
				return errors.New("新策略降低安全代次")
			}
		}
	}
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	return writeState(filepath.Join(dir, "security-policy.json"), b)
}
func (p ReleasePolicy) check(component, digest string, epoch int64) error {
	if epoch < p.MinEpoch[component] {
		return errors.New("版本低于发布安全代次")
	}
	for _, s := range p.Revoked {
		if s == digest {
			return errors.New("此程序已被发布者撤销")
		}
	}
	return nil
}
func refreshReleasePolicy(ctx context.Context, u *updater.Updater, c *http.Client, base, dir string) (ReleasePolicy, error) {
	var p ReleasePolicy
	info, e := u.GetTargetInfo("security-policy.json")
	if e != nil {
		return p, errors.New("受信目录缺少撤销策略")
	}
	if info.Length > 1<<20 {
		return p, errors.New("撤销策略过大")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/targets/security-policy.json", nil)
	if e != nil {
		return p, e
	}
	r, e := c.Do(req)
	if e != nil {
		return p, errors.New("无法读取发布撤销策略")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return p, errors.New("无法读取发布撤销策略")
	}
	b, e := safehttp.ReadBounded(r.Body, 1<<20)
	if e != nil {
		return p, e
	}
	if e = info.VerifyLengthHashes(b); e != nil {
		return p, errors.New("撤销策略目标校验失败")
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return p, e
	}
	return p, acceptReleasePolicy(dir, p)
}
