// Package secureupdate is the independent, root-owned code trust boundary.
// A controller-provided checksum is never an authority for executing code.
package secureupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/safehttp"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
	"golang.org/x/sys/unix"
)

const PolicyPath = "/etc/ctlvps/security.json"
const StateDir = "/var/lib/ctlvps-security"

var ErrUnconfigured = errors.New("本机自定义安全策略不可读或无效，请检查权限和内容")

type Policy struct {
	ChecksumOnly    bool                           `json:"-"`
	MaxNodes        int                            `json:"max_nodes,omitempty"`
	MaxMemoryMB     int                            `json:"max_memory_mb,omitempty"`
	ACMEDomains     []string                       `json:"acme_domains,omitempty"`
	Schema          int                            `json:"schema"`
	PrivateMetadata bool                           `json:"private_metadata,omitempty"`
	PauseConfig     bool                           `json:"pause_config,omitempty"`
	PrivateNodes    []int64                        `json:"private_nodes,omitempty"`
	TransportGrants []networkconfig.TransportGrant `json:"transport_grants,omitempty"`
	ForwardGrants   []networkconfig.ForwardGrant   `json:"forward_grants,omitempty"`
	MetadataURL     string                         `json:"metadata_url"`
	RootFile        string                         `json:"root_file"`
	MinEpoch        map[string]int64               `json:"min_epoch"`
	Actions         []string                       `json:"actions"`
	Certificates    map[string]Certificate         `json:"certificates,omitempty"`
}
type Certificate struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}
type Identity struct {
	Product   string `json:"product"`
	Component string `json:"component"`
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	Epoch     int64  `json:"epoch"`
}

func protected(path string) ([]byte, error) {
	if e := protectedParents(filepath.Dir(path)); e != nil {
		return nil, e
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	var stat unix.Stat_t
	if e = unix.Fstat(fd, &stat); e != nil || stat.Uid != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		f.Close()
		return nil, errors.New("安全配置必须为 root 拥有且不可被组/其他用户修改的普通文件")
	}
	defer f.Close()
	return safehttp.ReadBounded(f, 1<<20)
}
func protectedParents(path string) error {
	for {
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		var stat unix.Stat_t
		if e = unix.Lstat(path, &stat); e != nil || !st.IsDir() || stat.Uid != 0 || st.Mode().Perm()&0022 != 0 {
			return errors.New("安全配置父目录权限不安全")
		}
		if path == "/" {
			return nil
		}
		path = filepath.Dir(path)
	}
}
func LoadPolicy() (Policy, error) {
	var p Policy
	b, e := protected(PolicyPath)
	if os.IsNotExist(e) {
		return defaultPolicy(), nil
	}
	if e != nil {
		return p, ErrUnconfigured
	}
	return decodePolicy(b)
}

func decodePolicy(b []byte) (Policy, error) {
	var p Policy
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e := d.Decode(&p); e != nil {
		return p, e
	}
	if d.Decode(new(any)) != io.EOF {
		return Policy{}, ErrUnconfigured
	}
	if p.Schema != 1 {
		return p, ErrUnconfigured
	}
	if err := networkconfig.ValidateTransportGrants(p.TransportGrants); err != nil {
		return Policy{}, ErrUnconfigured
	}
	if err := networkconfig.ValidateForwardGrants(p.ForwardGrants); err != nil {
		return Policy{}, ErrUnconfigured
	}
	if p.MetadataURL == "" && p.RootFile == "" {
		p.ChecksumOnly = true
		return p, nil
	}
	u, e := url.Parse(p.MetadataURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return p, ErrUnconfigured
	}
	if !filepath.IsAbs(p.RootFile) {
		return p, ErrUnconfigured
	}
	return p, nil
}
func Allow(action string) error {
	p, e := LoadPolicy()
	if e != nil {
		return e
	}
	for _, a := range p.Actions {
		if a == action {
			return nil
		}
	}
	return errors.New("本机安全策略未授权此操作")
}

type Verifier struct {
	Policy Policy
	Root   []byte
	Dir    string
	Client *http.Client
}

type contextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t contextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r.Clone(t.ctx))
}

func Verify(ctx context.Context, component, version string, data []byte) error {
	return VerifyReader(ctx, component, version, bytes.NewReader(data))
}

// VerifyReader verifies a caller-owned immutable staging file without loading
// its contents into the heap. The caller keeps it private until installation.
func VerifyReader(ctx context.Context, component, version string, data io.ReadSeeker) error {
	p, e := LoadPolicy()
	if e != nil {
		return e
	}
	if p.ChecksumOnly {
		return verifyChecksumReader(ctx, component, version, data)
	}
	root, e := protected(p.RootFile)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(StateDir, 0700); e != nil {
		return e
	}
	if e = protectedParents(StateDir); e != nil {
		return e
	}
	v := Verifier{Policy: p, Root: root, Dir: StateDir}
	return v.VerifyReader(ctx, component, version, runtime.GOARCH, data)
}

// Verify authenticates TUF metadata, checks component identity and persists the
// anti-rollback security epoch before returning permission to execute bytes.
func (v *Verifier) Verify(ctx context.Context, component, version, arch string, data []byte) error {
	return v.VerifyReader(ctx, component, version, arch, bytes.NewReader(data))
}
func (v *Verifier) VerifyReader(ctx context.Context, component, version, arch string, data io.ReadSeeker) error {
	size, hashes, e := artifactHashes(data)
	if e != nil {
		return e
	}
	if component != "agent" && component != "controller" && component != "sing-box" && component != "snell-server" && component != "mita" && component != "installer" && component != "verifier" {
		return errors.New("未知更新组件")
	}
	if e := os.MkdirAll(v.Dir, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(v.Dir, "verify.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return errors.New("发布验证繁忙")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	md := filepath.Join(v.Dir, "metadata")
	root := v.Root
	if b, e := os.ReadFile(filepath.Join(md, "root.json")); e == nil {
		root = b
	} else if !os.IsNotExist(e) {
		return e
	}
	cfg, e := config.New(v.Policy.MetadataURL, root)
	if e != nil {
		return e
	}
	cfg.LocalMetadataDir = md
	cfg.LocalTargetsDir = filepath.Join(v.Dir, "targets")
	cfg.MaxRootRotations = 32
	cfg.MaxDelegations = 8
	c := v.Client
	if c == nil {
		opts := safehttp.Options{}
		if v.Policy.PrivateMetadata {
			opts.PrivateOrigins = []string{v.Policy.MetadataURL}
		}
		c = safehttp.New(opts)
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			c.Transport = agentnet.Transport{Public: true, PrivateOrigin: v.Policy.PrivateMetadata}
		}
	}
	copyClient := *c
	tr := c.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	copyClient.Transport = contextTransport{ctx, tr}
	if e = cfg.SetDefaultFetcherHTTPClient(&copyClient); e != nil {
		return e
	}
	u, e := updater.New(cfg)
	if e != nil {
		return fmt.Errorf("发布信任根无效: %w", e)
	}
	if e = u.Refresh(); e != nil {
		return errors.New("发布元数据验证失败或已过期")
	}
	releasePolicy, e := refreshReleasePolicy(ctx, u, &copyClient, v.Policy.MetadataURL, v.Dir)
	if e != nil {
		return e
	}
	digest := hex.EncodeToString(hashes["sha256"])
	target, e := u.GetTargetInfo("sha256/" + digest)
	if e != nil {
		return errors.New("文件不在受信发布目录中")
	}
	if target.Length != size || len(target.Hashes) == 0 {
		return errors.New("受信文件长度或哈希缺失")
	}
	for algorithm, want := range target.Hashes {
		if !bytes.Equal(hashes[algorithm], want) || hashes[algorithm] == nil {
			return errors.New("受信文件校验失败")
		}
	}
	var id Identity
	if target.Custom == nil || json.Unmarshal(*target.Custom, &id) != nil {
		return errors.New("发布身份信息缺失")
	}
	if id.Product != "VpsCT" || id.Component != component || (id.Arch != arch && !(id.Arch == "all" && id.Component == "installer")) || (version != "" && id.Version != version) || id.Version == "" || id.Epoch < 1 {
		return errors.New("发布组件、架构或版本不匹配")
	}
	if e = releasePolicy.check(component, digest, id.Epoch); e != nil {
		return e
	}
	if component == "controller" {
		if e = recordArchiveReader(v.Dir, digest, data); e != nil {
			return e
		}
	}
	floors := map[string]int64{}
	floorPath := filepath.Join(v.Dir, "epochs.json")
	if b, e := os.ReadFile(floorPath); e == nil {
		if json.Unmarshal(b, &floors) != nil {
			return errors.New("安全代次记录损坏")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	floor := max(floors[component], v.Policy.MinEpoch[component])
	if id.Epoch < floor {
		return errors.New("拒绝回退到已禁止的安全代次")
	}
	// Root-owned receipts permit offline rollback only within the current epoch floor.
	if e = os.MkdirAll(filepath.Join(v.Dir, "verified"), 0700); e != nil {
		return e
	}
	receipt, e := json.Marshal(id)
	if e != nil {
		return e
	}
	if e = writeState(filepath.Join(v.Dir, "verified", digest+".json"), receipt); e != nil {
		return e
	}
	floors[component] = max(floor, id.Epoch)
	b, e := json.Marshal(floors)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(v.Dir, ".epochs-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, floorPath); e != nil {
		return e
	}
	d, e := os.Open(v.Dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Hash both supported TUF algorithms in one bounded pass. Reject unknown
// target algorithms rather than silently weakening verification.
func artifactHashes(data io.ReadSeeker) (int64, map[string][]byte, error) {
	if _, err := data.Seek(0, io.SeekStart); err != nil {
		return 0, nil, err
	}
	h256, h512 := sha256.New(), sha512.New()
	n, err := safehttp.CopyBounded(io.MultiWriter(h256, h512), data, 256<<20)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		return 0, nil, errors.New("invalid release size")
	}
	return n, map[string][]byte{"sha256": h256.Sum(nil), "sha512": h512.Sum(nil)}, nil
}

// Entry exposes release integrity and recovery operations. The installer
// authenticates its helper through the official HTTPS checksum catalog first.
func Entry(args []string) (bool, error) {
	if len(args) == 5 && args[0] == "accept-checksum" {
		f, e := os.Open(args[4])
		if e != nil {
			return true, e
		}
		defer f.Close()
		return true, acceptChecksumReader(context.Background(), args[1], args[2], args[3], f)
	}
	if handled, err := recoveryEntry(args); handled {
		return true, err
	}
	if len(args) == 3 && args[0] == "verify-rollback" {
		p, e := LoadPolicy()
		if e != nil {
			return true, e
		}
		return true, CheckRollback(StateDir, p, args[1], runtime.GOARCH, args[2])
	}
	if len(args) == 0 || args[0] != "verify-release" {
		return false, nil
	}
	if len(args) != 4 {
		return true, errors.New("usage: verify-release COMPONENT VERSION FILE")
	}
	f, e := os.Open(args[3])
	if e != nil {
		return true, e
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if e = VerifyReader(ctx, args[1], args[2], f); e != nil {
		return true, e
	}
	if args[1] == "controller" {
		return true, validateArchiveReader(f)
	}
	return true, nil
}

// CheckRollback checks a locally verified digest, never a caller-supplied epoch.
func CheckRollback(dir string, p Policy, component, arch, digest string) error {
	return checkRollback(dir, p, component, arch, digest, false)
}
func checkRollback(dir string, p Policy, component, arch, digest string, inTransaction bool) error {
	b, e := hex.DecodeString(digest)
	if e != nil || len(b) != 32 {
		return errors.New("invalid rollback digest")
	}
	raw, e := os.ReadFile(filepath.Join(dir, "verified", strings.ToLower(digest)+".json"))
	if e != nil {
		return errors.New("rollback has no local verification receipt")
	}
	var id Identity
	if json.Unmarshal(raw, &id) != nil || id.Product != "VpsCT" || id.Component != component || id.Arch != arch {
		return errors.New("rollback identity mismatch")
	}
	if p.ChecksumOnly {
		return nil
	}
	rp, e := readReleasePolicy(dir)
	if e != nil {
		return e
	}
	if inTransaction {
		e = rp.validateStructure()
	} else {
		e = rp.validate()
	}
	if e != nil {
		return e
	}
	if e = rp.check(component, strings.ToLower(digest), id.Epoch); e != nil {
		return e
	}
	raw, e = os.ReadFile(filepath.Join(dir, "epochs.json"))
	if e != nil {
		return e
	}
	var floors map[string]int64
	if json.Unmarshal(raw, &floors) != nil || id.Epoch < max(floors[component], p.MinEpoch[component]) {
		return errors.New("rollback is below current security floor")
	}
	return nil
}
func VerifyAgentRollback(data []byte) error {
	p, e := LoadPolicy()
	if e != nil {
		return e
	}
	sum := sha256.Sum256(data)
	return CheckRollback(StateDir, p, "agent", runtime.GOARCH, hex.EncodeToString(sum[:]))
}
