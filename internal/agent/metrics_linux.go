//go:build linux

package agent

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ctlvps/internal/agentproto"
)

type cpuSample struct {
	idle, total uint64
	at          time.Time
}

// MetricsCollector reads /proc on Linux.
type MetricsCollector struct {
	prevCPU cpuSample
	prevNet struct {
		rx, tx int64
		at     time.Time
	}
	iface string
}

// NewMetricsCollector builds a collector.
func NewMetricsCollector() *MetricsCollector { return &MetricsCollector{} }

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func (m *MetricsCollector) cpu() (uint64, uint64) {
	for _, line := range strings.Split(readFile("/proc/stat"), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)
		var total, idle uint64
		for i := 1; i < len(f); i++ {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			total += v
			if i == 4 || i == 5 { // idle + iowait
				idle += v
			}
		}
		return idle, total
	}
	return 0, 0
}

// defaultInterface finds the interface of the default IPv4 route.
func defaultInterface() string {
	for i, line := range strings.Split(readFile("/proc/net/route"), "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

func netCounters(iface string) (rx, tx int64) {
	for i, line := range strings.Split(readFile("/proc/net/dev"), "\n") {
		if i < 2 {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if !legacyNetworkIncluded(name, iface) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseInt(f[0], 10, 64)
		t, _ := strconv.ParseInt(f[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func meminfo() map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(readFile("/proc/meminfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64)
		out[k] = n * 1024
	}
	return out
}

func countLines(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := -1 // header
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	if n < 0 {
		return 0
	}
	return n
}

// Collect returns a metrics snapshot.
func (m *MetricsCollector) Collect() agentproto.Metrics {
	now := time.Now()
	var out agentproto.Metrics
	idle, total := m.cpu()
	if m.prevCPU.total > 0 && total > m.prevCPU.total {
		dt := float64(total - m.prevCPU.total)
		di := float64(idle - m.prevCPU.idle)
		out.CPUPercent = (1 - di/dt) * 100
	}
	m.prevCPU = cpuSample{idle: idle, total: total, at: now}

	if f := strings.Fields(readFile("/proc/loadavg")); len(f) >= 2 {
		out.Load1, _ = strconv.ParseFloat(f[0], 64)
		out.Load5, _ = strconv.ParseFloat(f[1], 64)
	}
	mi := meminfo()
	out.MemTotal = mi["MemTotal"]
	if avail, ok := mi["MemAvailable"]; ok {
		out.MemUsed = out.MemTotal - avail
	} else {
		out.MemUsed = out.MemTotal - mi["MemFree"] - mi["Buffers"] - mi["Cached"]
	}
	out.SwapTotal = mi["SwapTotal"]
	out.SwapUsed = mi["SwapTotal"] - mi["SwapFree"]

	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err == nil {
		out.DiskTotal = int64(st.Blocks) * int64(st.Bsize)
		out.DiskUsed = out.DiskTotal - int64(st.Bavail)*int64(st.Bsize)
	}
	if f := strings.Fields(readFile("/proc/uptime")); len(f) >= 1 {
		up, _ := strconv.ParseFloat(f[0], 64)
		out.UptimeSec = int64(up)
	}
	if m.iface == "" {
		m.iface = defaultInterface()
	}
	out.Interface = m.iface
	out.NetRx, out.NetTx = netCounters(m.iface)
	if !m.prevNet.at.IsZero() {
		dt := now.Sub(m.prevNet.at).Seconds()
		if dt > 0 && out.NetRx >= m.prevNet.rx && out.NetTx >= m.prevNet.tx {
			out.NetRxRate = int64(float64(out.NetRx-m.prevNet.rx) / dt)
			out.NetTxRate = int64(float64(out.NetTx-m.prevNet.tx) / dt)
		}
	}
	m.prevNet.rx, m.prevNet.tx, m.prevNet.at = out.NetRx, out.NetTx, now
	out.TCPConns = countLines("/proc/net/tcp") + countLines("/proc/net/tcp6")
	out.UDPConns = countLines("/proc/net/udp") + countLines("/proc/net/udp6")
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				if _, err := strconv.Atoi(e.Name()); err == nil {
					out.Processes++
				}
			}
		}
	}
	out.Hostname, _ = os.Hostname()
	out.Kernel = strings.TrimSpace(readFile("/proc/sys/kernel/osrelease"))
	out.Arch = runtime.GOARCH
	return out
}

// BootID identifies the current boot (counters reset on reboot).
func BootID() string {
	return strings.TrimSpace(readFile("/proc/sys/kernel/random/boot_id"))
}
