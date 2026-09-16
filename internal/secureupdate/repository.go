package secureupdate

// Offline publishing helpers. Never called by ctlvpsd or an agent at runtime.
import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type ReleaseFile struct {
	Path     string   `json:"path"`
	Identity Identity `json:"identity"`
}

// InitRepository creates independent role keys in a new, private directory.
// The root key must subsequently be removed to offline storage by the operator.
func InitRepository(dir string) error {
	if e := os.Mkdir(dir, 0700); e != nil {
		return e
	}
	r := metadata.Root(time.Now().AddDate(1, 0, 0))
	r.Signed.ConsistentSnapshot = true
	keys := map[string]ed25519.PrivateKey{}
	for _, role := range []string{"root-1", "root-2", "root-3", "targets", "snapshot", "timestamp"} {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		keys[role] = key
		pub, e := metadata.KeyFromPublicKey(key.Public())
		if e != nil {
			return e
		}
		roleName := role
		if strings.HasPrefix(role, "root-") {
			roleName = "root"
		}
		if e = r.Signed.AddKey(pub, roleName); e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, role+".key"), []byte(hex.EncodeToString(key)), 0600); e != nil {
			return e
		}
	}
	r.Signed.Roles["root"].Threshold = 2
	for _, name := range []string{"root-1", "root-2", "root-3"} {
		signer, e := signature.LoadSigner(keys[name], crypto.Hash(0))
		if e != nil {
			return e
		}
		if _, e = r.Sign(signer); e != nil {
			return e
		}
	}
	b, e := r.ToBytes(true)
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, "root.json"), b, 0644)
}

func loadSigner(dir, role string) (signature.Signer, error) {
	p := filepath.Join(dir, role+".key")
	st, e := os.Lstat(p)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("发布私钥权限必须为 0600")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, e
	}
	raw, e := hex.DecodeString(string(b))
	if e != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("发布密钥无效")
	}
	return signature.LoadSigner(ed25519.PrivateKey(raw), crypto.Hash(0))
}

// PublishRepository writes a complete metadata generation into a NEW directory.
// Callers publish timestamp.json last; the existing release is never overwritten.
func PublishRepository(keys, output string, files []ReleaseFile, version int64, expiry time.Time) error {
	if version < 1 || len(files) == 0 || !expiry.After(time.Now()) || expiry.After(time.Now().Add(31*24*time.Hour)) {
		return errors.New("invalid metadata version, files or expiry")
	}
	root, e := metadata.Root().FromFile(filepath.Join(keys, "root.json"))
	if e != nil {
		return e
	}
	t := metadata.Targets(expiry)
	t.Signed.Version = version
	for _, f := range files {
		if f.Identity.Product != "VpsCT" || f.Identity.Version == "" || f.Identity.Epoch < 1 || (f.Identity.Arch != "amd64" && f.Identity.Arch != "arm64" && !(f.Identity.Arch == "all" && f.Identity.Component == "installer")) {
			return errors.New("invalid target identity")
		}
		data, e := os.ReadFile(f.Path)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(data)
		name := "sha256/" + hex.EncodeToString(sum[:])
		info, e := metadata.TargetFile().FromBytes(name, data, "sha256")
		if e != nil {
			return e
		}
		custom, e := json.Marshal(f.Identity)
		if e != nil {
			return e
		}
		raw := json.RawMessage(custom)
		info.Custom = &raw
		if _, ok := t.Signed.Targets[name]; ok {
			return errors.New("duplicate target content")
		}
		t.Signed.Targets[name] = info
	}
	policy := ReleasePolicy{Schema: 1, Product: "VpsCT", Sequence: version, Expires: minTime(expiry, time.Now().Add(7*24*time.Hour)), MinEpoch: map[string]int64{}, Revoked: []string{}}
	if b, err := os.ReadFile(filepath.Join(keys, "security-policy.json")); err == nil {
		if err = json.Unmarshal(b, &policy); err != nil {
			return err
		}
		policy.Sequence = version
		policy.Expires = minTime(expiry, time.Now().Add(7*24*time.Hour))
	} else if !os.IsNotExist(err) {
		return err
	}
	if e = policy.validate(); e != nil {
		return e
	}
	policyBytes, e := json.Marshal(policy)
	if e != nil {
		return e
	}
	policyTarget, e := metadata.TargetFile().FromBytes("security-policy.json", policyBytes, "sha256")
	if e != nil {
		return e
	}
	t.Signed.Targets["security-policy.json"] = policyTarget
	ts, e := loadSigner(keys, "targets")
	if e != nil {
		return e
	}
	if _, e = t.Sign(ts); e != nil {
		return e
	}
	if e = root.VerifyDelegate("targets", t); e != nil {
		return e
	}
	tb, e := t.ToBytes(true)
	if e != nil {
		return e
	}
	s := metadata.Snapshot(expiry)
	s.Signed.Version = version
	s.Signed.Meta["targets.json"] = metadata.MetaFile(version)
	th := sha256.Sum256(tb)
	s.Signed.Meta["targets.json"].Length = int64(len(tb))
	s.Signed.Meta["targets.json"].Hashes = map[string]metadata.HexBytes{"sha256": th[:]}
	ss, e := loadSigner(keys, "snapshot")
	if e != nil {
		return e
	}
	if _, e = s.Sign(ss); e != nil {
		return e
	}
	if e = root.VerifyDelegate("snapshot", s); e != nil {
		return e
	}
	sb, e := s.ToBytes(true)
	if e != nil {
		return e
	}
	z := metadata.Timestamp(minTime(expiry, time.Now().Add(7*24*time.Hour)))
	z.Signed.Version = version
	z.Signed.Meta["snapshot.json"] = metadata.MetaFile(version)
	sh := sha256.Sum256(sb)
	z.Signed.Meta["snapshot.json"].Length = int64(len(sb))
	z.Signed.Meta["snapshot.json"].Hashes = map[string]metadata.HexBytes{"sha256": sh[:]}
	zs, e := loadSigner(keys, "timestamp")
	if e != nil {
		return e
	}
	if _, e = z.Sign(zs); e != nil {
		return e
	}
	if e = root.VerifyDelegate("timestamp", z); e != nil {
		return e
	}
	zb, e := z.ToBytes(true)
	if e != nil {
		return e
	}
	if e = os.Mkdir(output, 0755); e != nil {
		return e
	}
	if e = os.Mkdir(filepath.Join(output, "targets"), 0755); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(output, "targets", "security-policy.json"), policyBytes, 0644); e != nil {
		return e
	}
	rb, e := root.ToBytes(true)
	if e != nil {
		return e
	}
	for name, b := range map[string][]byte{fmt.Sprintf("%d.root.json", root.Signed.Version): rb, "root.json": rb, fmt.Sprintf("%d.targets.json", version): tb, fmt.Sprintf("%d.snapshot.json", version): sb, "timestamp.json": zb} {
		if e = os.WriteFile(filepath.Join(output, name), b, 0644); e != nil {
			return e
		}
	}
	history, _ := filepath.Glob(filepath.Join(keys, "*.root.json"))
	for _, p := range history {
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(output, filepath.Base(p)), b, 0644); e != nil {
			return e
		}
	}
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// RotateRoot creates new role keys and a root signed by BOTH the old and new
// root authorities. The old private root is used only by this offline command.
func RotateRoot(previous, next string) error {
	old, e := metadata.Root().FromFile(filepath.Join(previous, "root.json"))
	if e != nil {
		return e
	}
	if e = InitRepository(next); e != nil {
		return e
	}
	root, e := metadata.Root().FromFile(filepath.Join(next, "root.json"))
	if e != nil {
		return e
	}
	root.Signed.Version = old.Signed.Version + 1
	root.Signatures = nil
	for _, dir := range []string{previous, next} {
		for _, name := range []string{"root", "root-1", "root-2", "root-3"} {
			signer, err := loadSigner(dir, name)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if _, e = root.Sign(signer); e != nil {
				return e
			}
		}
	}
	if e = old.VerifyDelegate("root", root); e != nil {
		return e
	}
	if e = root.VerifyDelegate("root", root); e != nil {
		return e
	}
	b, e := root.ToBytes(true)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(next, "root.json"), b, 0644); e != nil {
		return e
	}
	histories, _ := filepath.Glob(filepath.Join(previous, "*.root.json"))
	for _, p := range histories {
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(next, filepath.Base(p)), b, 0644); e != nil {
			return e
		}
	}
	oldBytes, e := old.ToBytes(true)
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(next, fmt.Sprintf("%d.root.json", old.Signed.Version)), oldBytes, 0644)
}
