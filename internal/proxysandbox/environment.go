package proxysandbox

import (
	"os"

	"ctlvps/internal/agentbudget"
)

// Preserve only fixed runtime settings, never arbitrary inherited overrides.
func proxyEnvironment(bin string) []string {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/nonexistent", "GOMEMLIMIT=" + os.Getenv("GOMEMLIMIT")}
	if bin == "/opt/ctlvps/bin/sing-box" {
		// The pinned official Go runtime supports this process-local workaround.
		// Sparse THP-backed heaps otherwise inflate RSS on Linux/AMD64 runners
		// despite GOMEMLIMIT. Do not change the host's global THP policy.
		env = append(env, "GODEBUG="+agentbudget.SingBoxGoDebug)
	}
	if bin == "/opt/ctlvps/bin/mita" {
		env = append(env, "MITA_CONFIG_JSON_FILE="+os.Getenv("MITA_CONFIG_JSON_FILE"), "MITA_UDS_PATH=/tmp/ctlvps-mita.sock", "MITA_INSECURE_UDS=1")
	}
	return env
}
