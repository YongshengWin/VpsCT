package agent

import (
	"os"
	"runtime"

	"ctlvps/internal/maintenance"
)

func lockConfiguration() (func(), error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return func() {}, nil
	}
	return maintenance.NewManager().ConfigurationLock()
}
