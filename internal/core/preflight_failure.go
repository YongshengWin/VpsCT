package core

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"ctlvps/internal/boundedexec"
)

// The preflight unit name is reused. Never classify an error using logs from a
// previous invocation; in particular, an old SO_MARK failure must not mask a new
// configuration failure. Missing or invalid invocation IDs mean no diagnosis.
func (s *Systemd) proxyPreflightJournal(ctx context.Context, unit, previous string) []string {
	id, err := s.ctl(ctx, "show", unit, "--property=InvocationID", "--value")
	id = strings.TrimSpace(id)
	decoded, decodeErr := hex.DecodeString(id)
	if err != nil || decodeErr != nil || len(id) != 32 || len(decoded) != 16 || id == strings.Repeat("0", 32) || id == strings.TrimSpace(previous) {
		return nil
	}
	// Plain stderr is not necessarily assigned warning/error priority by
	// journald. Invocation scoping is the boundary; do not filter its severity.
	out, _, err := boundedexec.Run(ctx, "", 16<<10, "journalctl", "-u", unit, "_SYSTEMD_INVOCATION_ID="+id, "-n", "20", "--no-pager", "-o", "cat")
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// Return only known diagnostic text to the controller. Raw core output may
// include user configuration; keep it in the local journal rather than reports.
func proxyPreflightError(output string) error {
	switch {
	case strings.Contains(output, "[proxy_sandbox_unavailable]"):
		return errors.New("[proxy_sandbox_unavailable] 当前内核或容器不支持所需 seccomp 隔离；请检查运行环境")
	case strings.Contains(output, "[proxy_mark_unavailable]"):
		return errors.New("[proxy_mark_unavailable] 当前内核或容器不允许专用代理账号使用 SO_MARK；请检查宿主内核与容器能力")
	default:
		return errors.New("[proxy_preflight_failed] 代理配置或隔离预检失败；请查看本机本次预检日志，修复后重试")
	}
}
