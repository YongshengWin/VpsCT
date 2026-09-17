package core

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/agentwork"
	"ctlvps/internal/safehttp"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type installRequest struct {
	BinDir, Name string
	Version      agentproto.CoreVersion
}

func installBinary(ctx context.Context, binDir, name string, v agentproto.CoreVersion) error {
	if !agentwork.Available() {
		return installBinaryInline(ctx, binDir, name, v)
	}
	b, err := json.Marshal(installRequest{binDir, name, v})
	if err != nil {
		return err
	}
	return agentwork.Start(ctx, "core-install", b)
}
func InstallEntry(args []string) (bool, error) {
	if len(args) != 1 || args[0] != "core-install" {
		return false, nil
	}
	if os.Geteuid() != 0 {
		return true, errors.New("resource worker requires root")
	}
	b, err := safehttp.ReadBounded(os.Stdin, 64<<10)
	if err != nil {
		return true, err
	}
	var r installRequest
	if err = json.Unmarshal(b, &r); err != nil {
		return true, err
	}
	if (r.Name != "sing-box" && r.Name != "snell-server") || !filepath.IsAbs(r.BinDir) || filepath.Clean(r.BinDir) != r.BinDir {
		return true, errors.New("invalid install request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return true, installBinaryInline(ctx, r.BinDir, r.Name, r.Version)
}
