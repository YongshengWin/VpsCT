//go:build linux

package agentnet

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if handled, err := Entry(os.Args[1:]); handled {
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Explicit opt-in: creates a dedicated identity and installs a fixture CA only
// inside the runner's disposable, network-disabled container.
func TestStreamingMemoryContainer(t *testing.T) {
	if os.Getenv("CTLVPS_MEMORY_TEST") != "1" {
		t.Skip("disposable container required")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil || os.Geteuid() != 0 {
		t.Fatal("container root required")
	}
	slowStarted, slowRelease := make(chan struct{}), make(chan struct{})
	defer close(slowRelease)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/v1/heartbeat" {
			_, _ = w.Write([]byte(`{"server_time":"2026-09-17T00:00:00Z"}`))
			return
		}
		if r.URL.Path == "/dl/agent/linux-slow" {
			_, _ = w.Write([]byte("fixture"))
			w.(http.Flusher).Flush()
			close(slowStarted)
			select {
			case <-slowRelease:
			case <-r.Context().Done():
			}
			return
		}
		chunk := make([]byte, 32<<10)
		count := 128 << 20
		if r.URL.Path == "/api/agent/v1/desired" {
			count = 2 << 20
		}
		for sent := 0; sent < count; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile("/usr/local/share/ca-certificates/ctlvps-memory-fixture.crt", cert, 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("update-ca-certificates").CombinedOutput(); err != nil {
		t.Fatalf("fixture CA: %v %s", err, out)
	}
	// Bound aggregate allocations as well as the container's hard memory limit.
	// The TLS fixture server shares this process; its allocations are included.
	f, err := os.CreateTemp("/var/tmp", "artifact-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n, err := DownloadTo(context.Background(), srv.URL+"/dl/agent/linux-fixture", 128<<20, true, f)
	runtime.ReadMemStats(&after)
	if err != nil || n != 128<<20 {
		t.Fatalf("stream %d: %v", n, err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 32<<20 {
		t.Fatalf("stream allocated %d bytes for 128 MiB artifact", allocated)
	}
	t.Logf("128 MiB stream parent+TLS-server allocation: %d bytes", allocated)
	// A stalled download must not occupy the heartbeat request slot.
	slowCtx, cancelSlow := context.WithCancel(context.Background())
	defer cancelSlow()
	slowDone := make(chan error, 1)
	go func() {
		_, err := DownloadTo(slowCtx, srv.URL+"/dl/agent/linux-slow", 1<<20, true, io.Discard)
		slowDone <- err
	}()
	select {
	case <-slowStarted:
	case err := <-slowDone:
		t.Fatal("slow transfer failed before starting", err)
	case <-time.After(5 * time.Second):
		t.Fatal("slow transfer did not start")
	}
	hbCtx, cancelHB := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelHB()
	hb, _ := http.NewRequestWithContext(hbCtx, "POST", srv.URL+"/api/agent/v1/heartbeat", nil)
	began := time.Now()
	hbResp, err := (Transport{}).RoundTrip(hb)
	if err != nil {
		t.Fatal("heartbeat blocked by download", err)
	}
	hbResp.Body.Close()
	t.Logf("heartbeat during stalled download: %s", time.Since(began))
	cancelSlow()
	select {
	case <-slowDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled transfer did not exit")
	}
	if _, err = DownloadTo(context.Background(), srv.URL+"/dl/agent/linux-fixture", 1<<20, true, io.Discard); err == nil {
		t.Fatal("oversize accepted")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/agent/v1/desired", nil)
	resp, err := (Transport{}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || len(body) != 2<<20 || !bytes.Equal(body[:32], make([]byte, 32)) {
		t.Fatal("isolated response mismatch", err)
	}
	if raw, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmHWM:") {
				t.Log(line)
			}
		}
	}
	for _, name := range []string{"memory.peak", "memory.events"} {
		if raw, err := os.ReadFile("/sys/fs/cgroup/" + name); err == nil {
			t.Logf("%s: %s", name, raw)
		}
	}
}
