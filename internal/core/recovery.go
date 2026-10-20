package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/diskbudget"
	"ctlvps/internal/safehttp"
)

// This journal deliberately excludes nft rules, proxyguard, network leases,
// metering and root authorization. None of those may be reverted with a core.
type coreRecovery struct {
	Format    int            `json:"format"`
	Phase     string         `json:"phase"`
	OldPolicy string         `json:"old_policy,omitempty"`
	Policy    string         `json:"policy,omitempty"`
	Retained  string         `json:"retained,omitempty"`
	Files     []recoveryFile `json:"files"`
	Units     []recoveryUnit `json:"units,omitempty"`
}
type recoveryFile struct {
	Path   string      `json:"path"`
	Backup string      `json:"backup,omitempty"`
	SHA256 string      `json:"sha256,omitempty"`
	Mode   os.FileMode `json:"mode"`
	UID    int         `json:"uid"`
	GID    int         `json:"gid"`
	Exists bool        `json:"exists"`
}
type recoveryUnit struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Enabled bool   `json:"enabled"`
}

func recoveryDir(binary string) string { return binary + ".recovery" }
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func writeCoreRecovery(binary string, r *coreRecovery) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b) > 2<<20 {
		return errors.New("core recovery journal exceeds budget")
	}
	if _, err = WriteIfChanged(filepath.Join(recoveryDir(binary), "journal.json"), b, 0600); err != nil {
		return err
	}
	return syncDirectory(recoveryDir(binary))
}
func loadCoreRecovery(binary string) (*coreRecovery, error) {
	dir := recoveryDir(binary)
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe core recovery directory")
	}
	if owner, ok := st.Sys().(*syscall.Stat_t); !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("untrusted core recovery owner")
	}
	journal, err := os.Lstat(filepath.Join(dir, "journal.json"))
	if err != nil || !journal.Mode().IsRegular() || journal.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe core recovery journal")
	}
	f, err := os.Open(filepath.Join(dir, "journal.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := safehttp.ReadBounded(f, 2<<20)
	if err != nil {
		return nil, err
	}
	var r coreRecovery
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Format != 1 || (r.Phase != "installing" && r.Phase != "installed" && r.Phase != "preflight" && r.Phase != "applying" && r.Phase != "validated") || len(r.Files) > 8192 || len(r.Units) > 4096 {
		return nil, errors.New("invalid core recovery journal")
	}
	return &r, nil
}
func newCoreRecovery(binary, phase string) (*coreRecovery, error) {
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		return nil, err
	}
	if err := cleanupCompletedRecovery(binary); err != nil {
		return nil, err
	}
	if err := os.Mkdir(recoveryDir(binary), 0700); err != nil {
		return nil, err
	}
	policy, err := os.ReadFile(binary + ".policy")
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	r := &coreRecovery{Format: 1, Phase: phase, OldPolicy: strings.TrimSpace(string(policy))}
	// Persist an empty journal first so a crash while making backups is
	// distinguishable from a candidate that replaced the executable.
	if err = writeCoreRecovery(binary, r); err != nil {
		return nil, err
	}
	return r, syncDirectory(filepath.Dir(binary))
}
func cleanupCompletedRecovery(binary string) error {
	done := recoveryDir(binary) + ".done"
	st, err := os.Lstat(done)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("unsafe completed core recovery directory")
	}
	if err = os.RemoveAll(done); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(binary))
}
func snapshotCoreFile(binary string, r *coreRecovery, path string) error {
	for _, f := range r.Files {
		if f.Path == path {
			return nil
		}
	}
	f := recoveryFile{Path: path, UID: -1, GID: -1}
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		r.Files = append(r.Files, f)
		return writeCoreRecovery(binary, r)
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > 200<<20 {
		return errors.New("unsafe or oversized core recovery source")
	}
	f.Exists, f.Mode = true, st.Mode().Perm()
	if owner, ok := st.Sys().(*syscall.Stat_t); ok {
		f.UID, f.GID = int(owner.Uid), int(owner.Gid)
	}
	f.Backup = strconv.Itoa(len(r.Files)) + ".previous"
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(recoveryDir(binary), f.Backup), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = safehttp.CopyBounded(io.MultiWriter(dst, h), src, 200<<20)
	if err == nil {
		err = dst.Sync()
	}
	closeErr := dst.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	f.SHA256 = hex.EncodeToString(h.Sum(nil))
	r.Files = append(r.Files, f)
	return writeCoreRecovery(binary, r)
}

func prepareBinaryRecovery(binary string) (*coreRecovery, error) {
	if st, err := os.Lstat(binary); err == nil {
		if err := diskbudget.Check(filepath.Dir(binary), st.Size()+16<<20, 8); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	r, err := newCoreRecovery(binary, "installing")
	if err != nil {
		return nil, err
	}
	for _, p := range []string{binary, binary + ".trusted", binary + ".activate", binary + ".policy", binary + ".retained"} {
		if err = snapshotCoreFile(binary, r, p); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func coreUnitAllowed(binary, unit string) bool {
	instance := func(prefix string) bool {
		if !strings.HasPrefix(unit, prefix) || !strings.HasSuffix(unit, ".service") {
			return false
		}
		port := strings.TrimSuffix(strings.TrimPrefix(unit, prefix), ".service")
		value, err := strconv.Atoi(port)
		return err == nil && value > 0 && value <= 65535 && strconv.Itoa(value) == port
	}
	switch filepath.Base(binary) {
	case "sing-box":
		return unit == singboxUnit || unit == "ctlvps-singbox-private.service" || instance("ctlvps-singbox@")
	case "snell-server":
		return instance("ctlvps-snell@")
	case "mita":
		return instance("ctlvps-mita@")
	}
	return false
}
func coreConfigAllowed(binary, path string) bool {
	rel, err := filepath.Rel(proxyConfigRoot, path)
	if err != nil || strings.Contains(rel, "..") {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 {
		return false
	}
	switch filepath.Base(binary) {
	case "sing-box":
		return (parts[0] == "public" || parts[0] == "private") && parts[1] == "config.json"
	case "snell-server":
		return strings.HasPrefix(parts[0], "snell-") && parts[1] == "config.conf"
	case "mita":
		return strings.HasPrefix(parts[0], "mita-") && parts[1] == "config.json"
	}
	return false
}
func managedCoreUnits(ctx context.Context, binary string, sd *Systemd, wanted []string) []string {
	units := append([]string(nil), wanted...)
	pattern := "ctlvps-singbox*.service"
	if filepath.Base(binary) == "snell-server" {
		pattern = "ctlvps-snell@*.service"
	}
	if filepath.Base(binary) == "mita" {
		pattern = "ctlvps-mita@*.service"
	}
	units = append(units, sd.ListUnits(ctx, pattern)...)
	paths, _ := filepath.Glob(filepath.Join(sd.UnitDir, pattern))
	for _, p := range paths {
		units = append(units, filepath.Base(p))
	}
	seen := map[string]bool{}
	out := []string{}
	for _, u := range units {
		if coreUnitAllowed(binary, u) && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}
func corePolicyFingerprint(nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) string {
	ns := append([]agentproto.NodeSpec(nil), nodes...)
	fs := append([]agentproto.ForwardSpec(nil), forwards...)
	sort.Slice(ns, func(i, j int) bool { return ns[i].NodeID < ns[j].NodeID })
	sort.Slice(fs, func(i, j int) bool { return fs[i].ForwardID < fs[j].ForwardID })
	// Wire JSON excludes local authority. Include it explicitly so revoking
	// private access or a transport grant never resurrects an older service.
	native := make([]any, 0, len(ns))
	for _, n := range ns {
		credentials := map[string]any{}
		for _, key := range []string{"password", "uuid", "psk", "username", "users", "private_key", "public_key", "short_id", "peer_public_key", "preshared_key", "pre_shared_key", "reality_private_key", "reality_public_key", "reality_short_id", "obfs_password"} {
			if value, ok := n.Params[key]; ok {
				credentials[key] = value
			}
		}
		native = append(native, []any{n.NodeID, n.Protocol, n.Core, n.ShareID, n.Blocked, credentials, n.Network, n.AllowPrivate, n.TransportGrants, n.Retired})
	}
	forward := make([]any, 0, len(fs))
	for _, f := range fs {
		forward = append(forward, []any{f, f.ForwardGrants})
	}
	b, _ := json.Marshal([]any{native, forward})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// beginCoreApply runs before the first configuration/unit mutation. A previous
// unfinished activation is restored before any new desired state is attempted.
func beginCoreApply(ctx context.Context, binary string, sd *Systemd, policy string, units, configs []string) error {
	r, err := loadCoreRecovery(binary)
	if err != nil {
		return err
	}
	if r != nil && (r.Phase == "preflight" || r.Phase == "applying" || r.Phase == "validated") {
		err = rollbackCore(ctx, binary, sd, r, policy)
		return errors.Join(errors.New("interrupted core activation recovered; retry latest desired state"), err)
	}
	if r == nil {
		if r, err = newCoreRecovery(binary, "installed"); err != nil {
			return err
		}
		for _, p := range []string{binary + ".activate", binary + ".policy", binary + ".retained"} {
			if err = snapshotCoreFile(binary, r, p); err != nil {
				return err
			}
		}
	}
	r.Policy = policy
	r.Units = nil
	units = managedCoreUnits(ctx, binary, sd, units)
	for _, u := range units {
		p := filepath.Join(sd.UnitDir, u)
		if err = snapshotCoreFile(binary, r, p); err != nil {
			return err
		}
		enabled, _ := sd.ctl(ctx, "is-enabled", u)
		r.Units = append(r.Units, recoveryUnit{u, sd.IsActive(ctx, u), strings.TrimSpace(enabled) == "enabled"})
	}
	for _, p := range configs {
		if !coreConfigAllowed(binary, p) {
			return errors.New("invalid core configuration recovery path")
		}
		if err = snapshotCoreFile(binary, r, p); err != nil {
			return err
		}
	}
	r.Phase = "preflight"
	return writeCoreRecovery(binary, r)
}

func markCoreApplying(binary string) error {
	r, err := loadCoreRecovery(binary)
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("core activation has no recovery point")
	}
	if r.Phase == "applying" {
		return nil
	}
	if r.Phase != "preflight" {
		return errors.New("invalid core activation phase")
	}
	r.Phase = "applying"
	return writeCoreRecovery(binary, r)
}

func validateCoreRecovery(binary string, sd *Systemd, r *coreRecovery) error {
	for _, f := range r.Files {
		allowed := f.Path == binary || f.Path == binary+".trusted" || f.Path == binary+".activate" || f.Path == binary+".policy" || f.Path == binary+".retained" || coreConfigAllowed(binary, f.Path)
		if sd != nil && filepath.Dir(f.Path) == sd.UnitDir && coreUnitAllowed(binary, filepath.Base(f.Path)) {
			allowed = true
		}
		if !allowed || filepath.Clean(f.Path) != f.Path {
			return errors.New("invalid core recovery target")
		}
		if !f.Exists {
			continue
		}
		if f.Backup != filepath.Base(f.Backup) || !strings.HasSuffix(f.Backup, ".previous") {
			return errors.New("invalid core recovery backup")
		}
		p := filepath.Join(recoveryDir(binary), f.Backup)
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			return errors.New("unsafe core recovery backup")
		}
		h, err := os.Open(p)
		if err != nil {
			return err
		}
		digest, err := FileDigest(h)
		h.Close()
		if err != nil || digest != f.SHA256 {
			return errors.New("core recovery backup checksum mismatch")
		}
	}
	for _, u := range r.Units {
		if !coreUnitAllowed(binary, u.Name) {
			return errors.New("invalid core recovery unit")
		}
	}
	return nil
}
func restoreCoreFile(binary string, f recoveryFile) error {
	if !f.Exists {
		err := os.Remove(f.Path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(f.Path))
	}
	if st, err := os.Lstat(f.Path); err == nil && !st.Mode().IsRegular() {
		return errors.New("unsafe core restore target")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0755); err != nil {
		return err
	}
	src, err := os.Open(filepath.Join(recoveryDir(binary), f.Backup))
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.CreateTemp(filepath.Dir(f.Path), ".core-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(dst.Name())
	defer dst.Close()
	if _, err = safehttp.CopyBounded(dst, src, 200<<20); err != nil {
		return err
	}
	if err = dst.Chmod(f.Mode); err != nil {
		return err
	}
	if f.UID >= 0 && os.Geteuid() == 0 {
		if err = dst.Chown(f.UID, f.GID); err != nil {
			return err
		}
	}
	if err = dst.Sync(); err != nil {
		return err
	}
	if err = dst.Close(); err != nil {
		return err
	}
	if err = os.Rename(dst.Name(), f.Path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(f.Path))
}
func removeCoreRecovery(binary string) error {
	return removeCoreRecoveryWith(binary, os.Rename, os.RemoveAll, syncDirectory)
}

func removeCoreRecoveryWith(binary string, rename func(string, string) error, remove func(string) error, sync func(string) error) error {
	// Rename the completed journal atomically before removing its backups. A
	// crash during cleanup must never leave a partially deleted active journal.
	done := recoveryDir(binary) + ".done"
	if err := remove(done); err != nil {
		return err
	}
	if err := rename(recoveryDir(binary), done); err != nil {
		return err
	}
	parent := filepath.Dir(binary)
	if err := sync(parent); err != nil {
		// The retirement is not durable yet. Re-expose the complete journal
		// so a failed commit can still restore its candidate's files/services.
		restoreErr := rename(done, recoveryDir(binary))
		if restoreErr == nil {
			restoreErr = sync(parent)
		}
		return errors.Join(err, restoreErr)
	}
	// The durable rename is the commit point. Backup cleanup cannot make a
	// committed candidate fail: callers would otherwise falsely claim rollback
	// after its journal had already disappeared. An interrupted cleanup is
	// retried by cleanupCompletedRecovery before the next transaction.
	_ = remove(done)
	_ = sync(parent)
	return nil
}
func rollbackCore(ctx context.Context, binary string, sd *Systemd, r *coreRecovery, policy string) error {
	if err := validateCoreRecovery(binary, sd, r); err != nil {
		return err
	}
	restart := r.OldPolicy != "" && r.OldPolicy == policy
	mutated := r.Phase == "applying" || r.Phase == "validated"
	stopNeeded := mutated || (policy != "" && !restart)
	units := []string{}
	for _, u := range r.Units {
		if sd != nil {
			if _, err := os.Stat(filepath.Join(sd.UnitDir, u.Name)); err == nil || sd.IsActive(ctx, u.Name) {
				units = append(units, u.Name)
			}
		}
	}
	if sd != nil && stopNeeded {
		units = managedCoreUnits(ctx, binary, sd, units)
	}
	if sd != nil && len(units) > 0 && stopNeeded {
		if err := sd.StopUnits(ctx, units); err != nil {
			return err
		}
		for _, u := range r.Units {
			if restart && !u.Enabled {
				if _, err := os.Stat(filepath.Join(sd.UnitDir, u.Name)); err == nil {
					if _, err = sd.ctl(ctx, "disable", u.Name); err != nil {
						return err
					}
				}
			}
		}
		// A snapshot failure may leave only part of the unit list recorded.
		// Revoked or unproven authorization must still fence every current unit.
		if !restart {
			for _, unit := range units {
				if _, err := os.Stat(filepath.Join(sd.UnitDir, unit)); err == nil {
					if _, err = sd.ctl(ctx, "disable", unit); err != nil {
						return err
					}
				}
			}
		}
	}
	// Verify all backups before stopping or replacing anything. Keep the journal
	// until every restore succeeds, making interrupted recovery idempotent.
	for _, f := range r.Files {
		if err := restoreCoreFile(binary, f); err != nil {
			return err
		}
	}
	if sd != nil && len(units) > 0 && stopNeeded {
		if err := sd.DaemonReload(ctx); err != nil {
			return err
		}
		for _, u := range r.Units {
			if u.Enabled && restart {
				if _, err := sd.ctl(ctx, "enable", u.Name); err != nil {
					return err
				}
			}
			if restart && u.Active {
				if err := sd.StartUnits(ctx, []string{u.Name}); err != nil {
					return err
				}
			}
		}
		if restart {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			for _, u := range r.Units {
				if u.Active && !sd.IsActive(ctx, u.Name) {
					return errors.New("restored core service failed its running check")
				}
			}
		}
	}
	return removeCoreRecovery(binary)
}
func rollbackCoreAfterFailure(binary string, sd *Systemd, policy string, cause error) error {
	r, err := loadCoreRecovery(binary)
	if err != nil {
		return errors.Join(cause, err)
	}
	if r == nil {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(cause, rollbackCore(ctx, binary, sd, r, policy))
}
func markCoreValidated(binary string) error {
	r, err := loadCoreRecovery(binary)
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("core activation has no recovery point")
	}
	// The recovery point may be discarded only after the activated config,
	// unit and their directory entries have reached disk, including deletions.
	dirs := map[string]bool{}
	for _, file := range r.Files {
		parent := filepath.Dir(file.Path)
		if dirs[parent] {
			continue
		}
		if _, err := os.Stat(parent); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := syncDirectory(parent); err != nil {
			return err
		}
		dirs[parent] = true
	}
	r.Phase = "validated"
	return writeCoreRecovery(binary, r)
}
func finishCoreActivation(ctx context.Context, binary string, sd *Systemd, success bool) error {
	r, err := loadCoreRecovery(binary)
	if err != nil || r == nil {
		if err == nil && !success {
			if _, completedErr := os.Lstat(recoveryDir(binary) + ".done"); completedErr == nil {
				return errors.New("core recovery retirement is incomplete; rollback cannot be confirmed")
			} else if !os.IsNotExist(completedErr) {
				return completedErr
			}
		}
		return err
	}
	if !success {
		return rollbackCore(ctx, binary, sd, r, r.Policy)
	}
	if r.Phase != "validated" {
		return errors.New("cannot commit unvalidated core activation")
	}
	if _, err = WriteIfChanged(binary+".policy", []byte(r.Policy+"\n"), 0600); err != nil {
		return err
	}
	if r.Retained != "" {
		if _, err = WriteIfChanged(binary+".retained", []byte(r.Retained+"\n"), 0600); err != nil {
			return err
		}
	}
	if err = completeActivation(binary); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Dir(binary)); err != nil {
		return err
	}
	return removeCoreRecovery(binary)
}

// Installation can be killed before a receipt is written. Restore this phase
// before trusting recordedVersion; applying/validated phases are recovered by
// Apply with the current authorization fingerprint.
func recoverCoreInstallation(ctx context.Context, binary string, sd *Systemd) (bool, error) {
	if err := cleanupCompletedRecovery(binary); err != nil {
		return false, err
	}
	r, err := loadCoreRecovery(binary)
	if err != nil || r == nil {
		return false, err
	}
	if r.Phase == "installing" {
		return false, rollbackCore(ctx, binary, sd, r, r.OldPolicy)
	}
	return r.Phase == "preflight" || r.Phase == "applying" || r.Phase == "validated", nil
}

func discardStagedCore(ctx context.Context, binary string, sd *Systemd) error {
	r, err := loadCoreRecovery(binary)
	if err != nil || r == nil {
		return err
	}
	if r.Phase != "installed" {
		return errors.New("unfinished core activation must be recovered before installation")
	}
	return rollbackCore(ctx, binary, sd, r, r.OldPolicy)
}

func (d *SingBox) FinishActivation(ctx context.Context, success bool) error {
	return finishCoreActivation(ctx, d.bin(), d.Systemd, success)
}
func (d *Snell) FinishActivation(ctx context.Context, success bool) error {
	return finishCoreActivation(ctx, d.bin(), d.Systemd, success)
}
func (d *Mita) FinishActivation(ctx context.Context, success bool) error {
	return finishCoreActivation(ctx, d.bin(), d.Systemd, success)
}

// standaloneRecoveryPaths includes stopped instances and absent new files too.
func standaloneRecoveryPaths(ctx context.Context, binary string, sd *Systemd, nodes []agentproto.NodeSpec) ([]string, []string) {
	wanted := []string{}
	kind, prefix, file := "snell", "snell-", "config.conf"
	if filepath.Base(binary) == "mita" {
		kind, prefix, file = "mieru", "mita-", "config.json"
	}
	for _, n := range nodes {
		wanted = append(wanted, StandaloneUnit(kind, n.ListenPort))
	}
	units := managedCoreUnits(ctx, binary, sd, wanted)
	configs := []string{}
	for _, u := range units {
		_, tail, _ := strings.Cut(u, "@")
		port := strings.TrimSuffix(tail, ".service")
		if p, err := strconv.Atoi(port); err == nil && p > 0 && p <= 65535 {
			configs = append(configs, filepath.Join(proxyConfigRoot, prefix+port, file))
		}
	}
	return units, configs
}
