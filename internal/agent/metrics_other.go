//go:build !linux

package agent

import (
	"os"
	"runtime"
	"time"

	"ctlvps/internal/agentproto"
)

// MetricsCollector is a stub on non-Linux platforms (development only).
type MetricsCollector struct {
	start time.Time
}

// NewMetricsCollector builds a collector.
func NewMetricsCollector() *MetricsCollector { return &MetricsCollector{start: time.Now()} }

// Collect returns a mostly empty snapshot.
func (m *MetricsCollector) Collect() agentproto.Metrics {
	host, _ := os.Hostname()
	return agentproto.Metrics{Hostname: host, Arch: runtime.GOARCH, Kernel: runtime.GOOS, UptimeSec: int64(time.Since(m.start).Seconds())}
}

// BootID is static on non-Linux platforms.
func BootID() string { return "dev" }
