package agent

import (
	"errors"
	"math"
	"strings"

	"ctlvps/internal/agentproto"
)

// Keep the legacy filter unchanged during explicit migration. The new policy
// uses stable interface identities instead of these historical name filters.
func legacyNetworkIncluded(name, iface string) bool {
	return name != "lo" && !strings.HasPrefix(name, "docker") && !strings.HasPrefix(name, "veth") && !strings.HasPrefix(name, "br-") && !strings.HasPrefix(name, "tun") && !strings.HasPrefix(name, "wg") && (iface == "" || name == iface)
}

// Derive both sides of the cutover from one netlink counter snapshot. Reading
// /proc/net/dev and netlink separately would leave a gap between the old tail
// and the new baseline, even when selecting the same interface.
func legacyNetworkBoundary(snapshot *agentproto.NetworkSnapshot, iface, epoch string) (agentproto.LegacyNetworkCounters, error) {
	result := agentproto.LegacyNetworkCounters{Epoch: epoch}
	if snapshot == nil || (snapshot.Status != "ok" && snapshot.Status != "incomplete") || (iface == "" && snapshot.Status != "ok") {
		return result, errors.New("旧计费来源需要完整有效的切换采样")
	}
	found := iface == "" || !legacyNetworkIncluded(iface, iface)
	for _, n := range snapshot.Interfaces {
		if !legacyNetworkIncluded(n.Name, iface) {
			continue
		}
		if !n.CountersValid || n.Rx > math.MaxInt64/4-result.Rx || n.Tx > math.MaxInt64/4-result.Tx {
			return result, errors.New("旧计费来源计数无效")
		}
		found = true
		result.Rx += n.Rx
		result.Tx += n.Tx
	}
	if !found {
		return result, errors.New("旧计费接口已消失，无法确认切换边界")
	}
	return result, result.Validate()
}
