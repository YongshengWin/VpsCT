package proxyguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const policyFile = Directory + "/policy.json"

type policy struct {
	Boot   string   `json:"boot"`
	Rules  string   `json:"rules"`
	Digest string   `json:"digest"`
	Units  []string `json:"units"`
	Paused []string `json:"paused,omitempty"`
}

func command(ctx context.Context, input, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdin = strings.NewReader(input)
	b, e := c.CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("%s failed: %w", name, e)
	}
	return b, nil
}

// Serialize policy publication and recovery, including separate watchdog processes.
func locked(ctx context.Context, f func() error) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("proxy policy requires root")
	}
	if e := trusted("/run", true); e != nil {
		return e
	}
	if e := os.Mkdir(Directory, 0755); e != nil && !os.IsExist(e) {
		return e
	}
	if e := trusted(Directory, true); e != nil {
		return e
	}
	fd, e := unix.Open(Directory+"/policy.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	for {
		e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if e == nil {
			break
		}
		if e != unix.EWOULDBLOCK && e != unix.EAGAIN {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return f()
}
func writePolicy(p policy) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(Directory, ".policy-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), policyFile)
}
func readPolicy() (policy, error) {
	var p policy
	if e := trusted(policyFile, false); e != nil {
		return p, e
	}
	b, e := os.ReadFile(policyFile)
	if e != nil {
		return p, e
	}
	if len(b) > 4<<20 {
		return p, fmt.Errorf("oversized proxy policy")
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return p, e
	}
	current, e := boot()
	if e != nil {
		return p, e
	}
	if p.Boot != string(current) {
		return p, fmt.Errorf("stale proxy policy")
	}
	for _, u := range append(append([]string{}, p.Units...), p.Paused...) {
		if !validUnit(u) {
			return p, fmt.Errorf("invalid proxy unit")
		}
	}
	return p, nil
}
func validUnit(u string) bool {
	if u == "ctlvps-singbox.service" || u == "ctlvps-singbox-private.service" {
		return true
	}
	if !strings.HasPrefix(u, "ctlvps-snell@") || !strings.HasSuffix(u, ".service") {
		return false
	}
	s := strings.TrimSuffix(strings.TrimPrefix(u, "ctlvps-snell@"), ".service")
	n, e := strconv.Atoi(s)
	return e == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == s
}

// Rule handles and nft version metadata change on reload but not enforcement.
func digest(raw []byte) (string, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if e := json.Unmarshal(raw, &doc); e != nil {
		return "", e
	}
	out := []map[string]any{}
	for _, entry := range doc.Nftables {
		row := map[string]any{}
		for kind, b := range entry {
			if kind == "metainfo" {
				continue
			}
			var value map[string]any
			if e := json.Unmarshal(b, &value); e != nil {
				return "", e
			}
			delete(value, "handle")
			row[kind] = value
		}
		if len(row) > 0 {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return "", fmt.Errorf("empty firewall table")
	}
	b, e := json.Marshal(out)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func liveDigest(ctx context.Context) (string, error) {
	b, e := command(ctx, "", "nft", "-j", "list", "table", "inet", "ctlvps_egress")
	if e != nil {
		return "", e
	}
	return digest(b)
}

func activeUnits(ctx context.Context) ([]string, error) {
	b, e := command(ctx, "", "systemctl", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "--state=active,activating,reloading", "ctlvps-singbox*", "ctlvps-snell@*")
	if e != nil {
		return nil, e
	}
	var units []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && validUnit(f[0]) {
			units = append(units, f[0])
		}
	}
	return units, nil
}

func revoke() error {
	e := os.Remove(ReadyFile)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}

func stopUnits(ctx context.Context, units []string) error {
	var result error
	for _, u := range units {
		// Queue an explicit stop first to suppress Restart=always, then kill
		// the entire cgroup. An unresponsive or compromised proxy must not
		// keep forwarding for systemd's default graceful-stop timeout.
		_, stopErr := command(ctx, "", "systemctl", "stop", "--no-block", u)
		_, killErr := command(ctx, "", "systemctl", "kill", "--kill-who=all", "--signal=SIGKILL", u)
		// A fast graceful exit leaves nothing to kill and is also success.
		if killErr != nil {
			state, stateErr := command(ctx, "", "systemctl", "show", u, "-p", "ActiveState", "--value")
			if stateErr == nil && (strings.TrimSpace(string(state)) == "inactive" || strings.TrimSpace(string(state)) == "failed") {
				killErr = nil
			}
		}
		_, waitErr := command(ctx, "", "systemctl", "stop", u)
		result = errors.Join(result, stopErr, killErr, waitErr)
	}
	return result
}

// Install atomically installs generated rules and publishes their trusted snapshot.
func Install(ctx context.Context, rules string, units []string) error {
	return locked(ctx, func() error {
		for _, u := range units {
			if !validUnit(u) {
				return fmt.Errorf("invalid protected unit")
			}
		}
		// Recreate only the counter-free guard table so changed hook priorities
		// and obsolete chains cannot survive a policy update.
		transaction := "add table inet ctlvps_egress\ndelete table inet ctlvps_egress\n" + rules
		if _, e := command(ctx, transaction, "nft", "--check", "-f", "-"); e != nil {
			return e
		}
		if _, e := command(ctx, transaction, "nft", "-f", "-"); e != nil {
			return e
		}
		d, e := liveDigest(ctx)
		if e != nil {
			return e
		}
		b, e := boot()
		if e != nil {
			return e
		}
		sort.Strings(units)
		p := policy{Boot: string(b), Rules: rules, Digest: d, Units: units}
		// Do not revive nodes removed by a later desired state.
		if old, e := readPolicy(); e == nil {
			for _, u := range old.Paused {
				for _, v := range units {
					if u == v {
						p.Paused = append(p.Paused, u)
					}
				}
			}
		}
		if e = writePolicy(p); e != nil {
			return e
		}
		return Publish()
	})
}

// Check repairs drift locally, even while the controller or agent is offline.
// Repair failure revokes startup readiness and pauses only managed proxy units.
func Check(ctx context.Context) error {
	return locked(ctx, func() error {
		p, e := readPolicy()
		if os.IsNotExist(e) && func() bool { _, e := os.Lstat(ReadyFile); return os.IsNotExist(e) }() {
			// Before initial setup there is nothing to protect. If volatile
			// state disappeared while proxies run, do not treat it as setup.
			safe, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			units, listErr := activeUnits(safe)
			if listErr == nil && len(units) == 0 {
				return nil
			}
			return errors.Join(e, listErr, stopUnits(safe, units))
		}
		if e != nil {
			// Do not trust a readiness marker if its root policy is missing/corrupt.
			safe, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			units, listErr := activeUnits(safe)
			return errors.Join(e, revoke(), listErr, stopUnits(safe, units))
		}
		d, readErr := liveDigest(ctx)
		if readErr != nil || d != p.Digest {
			// Egress has no billing counters; replace the whole table in one transaction.
			rules := "add table inet ctlvps_egress\ndelete table inet ctlvps_egress\n" + p.Rules
			_, repairErr := command(ctx, rules, "nft", "-f", "-")
			if repairErr == nil {
				d, repairErr = liveDigest(ctx)
				if repairErr == nil && d != p.Digest {
					repairErr = fmt.Errorf("restored policy differs")
				}
			}
			if repairErr != nil {
				// Reserve a fresh timeout for stopping after a timed-out nft command.
				safe, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				revokeErr := revoke()
				known := map[string]bool{}
				for _, u := range p.Paused {
					known[u] = true
				}
				active, listErr := activeUnits(safe)
				for _, u := range active {
					if !known[u] {
						p.Paused = append(p.Paused, u)
						known[u] = true
					}
				}
				// Persist the restart set before stopping; a killed watchdog can resume it.
				saveErr := writePolicy(p)
				stopErr := stopUnits(safe, p.Paused)
				return errors.Join(fmt.Errorf("proxy policy restore failed; proxies paused: %w", repairErr), revokeErr, saveErr, listErr, stopErr)
			}
		}
		if e = Publish(); e != nil {
			return e
		}
		var startErr error
		remaining := []string{}
		for _, u := range p.Paused {
			allowed := false
			for _, v := range p.Units {
				if u == v {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
			if _, e = command(ctx, "", "systemctl", "start", u); e != nil {
				remaining = append(remaining, u)
				startErr = errors.Join(startErr, e)
			}
		}
		if len(p.Paused) > 0 {
			p.Paused = remaining
			startErr = errors.Join(startErr, writePolicy(p))
		}
		return startErr
	})
}

func Entry(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "proxy-guard" {
		return false, nil
	}
	if len(args) != 1 {
		return true, fmt.Errorf("proxy-guard takes no arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return true, Check(ctx)
}
