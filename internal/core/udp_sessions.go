package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"ctlvps/internal/openrc"
	"ctlvps/internal/udpsession"
)

func (d *SingBox) udpSecret() (string, error) {
	path := filepath.Join(d.Paths.DataDir, "udp-session-secret")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		var key [32]byte
		_, err = rand.Read(key[:])
		if err == nil {
			_, err = f.WriteString(hex.EncodeToString(key[:]))
		}
		if err == nil {
			err = f.Sync()
		}
		ce := f.Close()
		if err == nil {
			err = ce
		}
		if err != nil {
			_ = os.Remove(path)
			return "", err
		}
	} else if !os.IsExist(err) {
		return "", err
	}
	b, err := readUDPConfig(path, 64)
	if err != nil || len(b) != 64 {
		return "", errors.New("invalid local UDP session secret")
	}
	if _, err = hex.DecodeString(string(b)); err != nil {
		return "", errors.New("invalid local UDP session secret")
	}
	st, err := os.Lstat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		return "", errors.New("UDP session secret must be owner-only")
	}
	return string(b), nil
}

func readUDPConfig(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Size() > limit {
		return nil, errors.New("untrusted local UDP configuration")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) {
		return nil, errors.New("local UDP configuration changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("local UDP configuration exceeds budget")
	}
	return b, err
}

func (s *Systemd) PublicProxyPID(ctx context.Context) (int, error) {
	if s.openRC {
		return openrc.MainPID(singboxUnit)
	}
	v, err := s.ctl(ctx, "show", singboxUnit, "--property=MainPID", "--value")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || pid < 0 {
		return 0, errors.New("invalid proxy process identity")
	}
	return pid, nil
}

func LoadUDPManager(ctx context.Context, sd *Systemd) (int, udpsession.Config, *udpsession.API, error) {
	raw, err := readUDPConfig(filepath.Join(proxyConfigRoot, "public", "config.json"), 2<<20)
	if os.IsNotExist(err) {
		return 0, udpsession.Config{}, nil, nil
	}
	if err != nil {
		pid, _ := sd.PublicProxyPID(ctx)
		return pid, udpsession.Config{}, nil, err
	}
	cfg, err := udpsession.ParseConfig(raw)
	if err == nil && len(cfg.Policies) == 0 {
		return 0, cfg, nil, nil
	}
	pid, pidErr := sd.PublicProxyPID(ctx)
	if pidErr != nil || pid == 0 {
		return pid, cfg, nil, pidErr
	}
	if err != nil {
		return pid, cfg, nil, err
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil || (exe != "/opt/ctlvps/bin/sing-box" && exe != "/opt/ctlvps/bin/sing-box (deleted)") {
		return pid, cfg, nil, errors.New("UDP manager core identity differs")
	}
	address, err := udpsession.Discover(pid, cfg.ListenPorts)
	if err != nil {
		return pid, cfg, nil, err
	}
	api, err := udpsession.NewAPI(address, cfg.Secret)
	return pid, cfg, api, err
}

func verifyUDPManager(ctx context.Context, sd *Systemd) error {
	_, cfg, api, err := LoadUDPManager(ctx, sd)
	if err != nil {
		return err
	}
	if api == nil {
		return errors.New("[udp_session_manager_unavailable] local connection manager not running")
	}
	if _, err = udpsession.LiveFor(ctx, nil); err != nil {
		return errors.New("[udp_session_manager_unavailable] kernel conntrack observation unavailable")
	}
	if _, err = api.Connections(ctx, cfg.Policies); err != nil {
		return errors.New("[udp_session_manager_unavailable] local connection observation unavailable")
	}
	return nil
}
