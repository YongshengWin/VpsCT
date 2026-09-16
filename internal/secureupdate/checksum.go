package secureupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"ctlvps/internal/agentnet"
)

const officialReleaseBase = "https://github.com/YongshengWin/VpsCT/releases/"

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$`)

func defaultPolicy() Policy {
	return Policy{Schema: 1, ChecksumOnly: true, Actions: []string{"agent.configure", "agent.update", "core.install", "controller.update", "agent.uninstall", "controller.uninstall", "agent.purge", "controller.purge"}, MinEpoch: map[string]int64{}}
}

// Checksum mode trusts the publisher's HTTPS endpoint. It does not claim
// independent signature authentication or signed revocation/epoch guarantees.
func verifyChecksum(ctx context.Context, component, version string, data []byte) error {
	if len(data) == 0 || len(data) > 256<<20 {
		return errors.New("invalid release size")
	}
	if version != "" && !releaseTag.MatchString(version) {
		return errors.New("invalid release version")
	}
	if component != "agent" && component != "controller" && component != "verifier" {
		return errors.New("unsupported release component")
	}
	base := officialReleaseBase + "latest/download/"
	if version != "" {
		base = officialReleaseBase + "download/" + version + "/"
	}
	name := "ctlvps-" + component + "-linux-" + runtime.GOARCH
	if component == "verifier" {
		name = "ctlvps-verify-linux-" + runtime.GOARCH
	}
	if component == "controller" {
		if version == "" {
			return errors.New("controller version required")
		}
		name = "ctlvps-" + version + "-linux-" + runtime.GOARCH + ".tar.gz"
	}
	sums, err := agentnet.Download(ctx, base+"SHA256SUMS", 1<<20, false)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if err = matchChecksum(sums, name, hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	return recordChecksum(StateDir, component, version, data)
}

func matchChecksum(sums []byte, name, digest string) error {
	matches := 0
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			matches++
			if !strings.EqualFold(fields[0], digest) {
				return errors.New("official release SHA256 mismatch")
			}
		}
	}
	if matches != 1 {
		return errors.New("missing or duplicate official release checksum")
	}
	return nil
}

func recordChecksum(dir, component, version string, data []byte) error {
	if component != "agent" && component != "controller" && component != "verifier" {
		return errors.New("unsupported release component")
	}
	if len(data) == 0 || len(data) > 256<<20 {
		return errors.New("invalid release size")
	}
	if err := os.MkdirAll(filepath.Join(dir, "verified"), 0700); err != nil {
		return err
	}
	if err := protectedParents(dir); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if component == "controller" {
		if err := recordArchive(dir, digest, data); err != nil {
			return err
		}
	}
	b, err := json.Marshal(Identity{Product: "VpsCT", Component: component, Version: version, Arch: runtime.GOARCH, Epoch: 1})
	if err != nil {
		return err
	}
	return writeState(filepath.Join(dir, "verified", digest+".json"), b)
}

// Used by the root installer after checking SHA256SUMS obtained with the archive.
// This records integrity for rollback; it does not create a publisher signature.
func acceptChecksum(ctx context.Context, component, version, digest string, data []byte) error {
	sum := sha256.Sum256(data)
	if !strings.EqualFold(digest, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("release SHA256 mismatch")
	}
	p, err := LoadPolicy()
	if err != nil {
		return err
	}
	if !p.ChecksumOnly {
		return Verify(ctx, component, version, data)
	}
	return recordChecksum(StateDir, component, version, data)
}
