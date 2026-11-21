package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestOfflineSingBoxCache(t *testing.T) {
	const filename = "sing-box-1.14.1-linux-amd64.tar.gz"
	const payload = "local release archive fixture"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	dir := filepath.Join(t.TempDir(), "core-cache")
	var out bytes.Buffer
	if present, _, err := copyOfflineSingBox(dir, filename, "", &out); present || err != nil {
		t.Fatalf("absent cache did not permit online installation: %v", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{filename: payload, filename + ".sha256": digest + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	present, sum, err := copyOfflineSingBox(dir, filename, digest, &out)
	if !present || err != nil || sum != digest || out.String() != payload {
		t.Fatalf("prepared archive was not selected: %v", err)
	}
	out.Reset()
	present, _, err = copyOfflineSingBox(dir, filename, strings.Repeat("0", 64), &out)
	if !present || err == nil || out.Len() != 0 {
		t.Fatalf("cache with conflicting checksum reached extraction: %v", err)
	}
	present, _, err = copyOfflineSingBox(dir, "sing-box-1.14.2-linux-amd64.tar.gz", "", &out)
	if !present || err == nil || out.Len() != 0 {
		t.Fatalf("missing release fell back to a different source: %v", err)
	}
}

func TestOfflineOfficialSingBoxInstall(t *testing.T) {
	if os.Getenv("CTLVPS_OFFLINE_CORE_TEST") != "1" {
		t.Skip("disposable Linux container and verified official archive required")
	}
	interfaces, err := os.ReadDir("/sys/class/net")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/.dockerenv"); err != nil || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" ||
		os.Geteuid() != 0 || len(interfaces) != 1 || interfaces[0].Name() != "lo" {
		t.Fatal("requires root in a disposable Linux AMD64 container with --network none")
	}
	const version = "1.14.1"
	url := officialSingBoxArchive(version, runtime.GOARCH, false)
	filename := filepath.Base(url)
	archive, err := os.ReadFile(filepath.Join("/fixtures", filename))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(offlineCoreDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(offlineCoreDir) })
	digest := fmt.Sprintf("%x", sha256.Sum256(archive))
	if err := os.WriteFile(filepath.Join(offlineCoreDir, filename), archive, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(offlineCoreDir, filename+".sha256")
	if err := os.WriteFile(manifest, []byte(digest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	pinned := agentproto.CoreVersion{Version: version, URL: url, SHA256: map[string]string{runtime.GOARCH: digest}}
	if err := installBinaryInline(context.Background(), bin, "sing-box", pinned); err != nil {
		t.Fatal(err)
	}
	if got := recordedVersion(filepath.Join(bin, "sing-box")); got != version {
		t.Fatalf("installed version = %q", got)
	}
	if err := os.WriteFile(manifest, []byte(strings.Repeat("0", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installBinaryInline(context.Background(), bin, "sing-box", pinned); err == nil {
		t.Fatal("wrong offline SHA256 accepted")
	}
	if got := recordedVersion(filepath.Join(bin, "sing-box")); got != version {
		t.Fatalf("failed install changed the active version to %q", got)
	}
}
