// Package proxyguard gates proxy startup on root-installed rules for this boot.
package proxyguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const Directory = "/run/ctlvps-proxy"
const ReadyFile = Directory + "/egress-ready"

func trusted(path string, directory bool) error {
	s, e := os.Lstat(path)
	if e != nil {
		return e
	}
	owner, ok := s.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || s.Mode().Perm()&0022 != 0 || s.Mode()&os.ModeSymlink != 0 || s.IsDir() != directory || (!directory && !s.Mode().IsRegular()) {
		return fmt.Errorf("untrusted proxy readiness path")
	}
	return nil
}

func boot() ([]byte, error) {
	b, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return nil, e
	}
	if len(strings.TrimSpace(string(b))) != 36 {
		return nil, fmt.Errorf("invalid boot identity")
	}
	return b, nil
}

func Publish() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("only root may publish proxy readiness")
	}
	if e := trusted("/run", true); e != nil {
		return e
	}
	if e := os.Mkdir(Directory, 0755); e != nil && !os.IsExist(e) {
		return e
	}
	if e := trusted(Directory, true); e != nil {
		return e
	}
	b, e := boot()
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(Directory, ".ready-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0644); e == nil {
		_, e = f.Write(b)
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), ReadyFile)
}

func Ready() error {
	for _, p := range []string{"/run", filepath.Dir(ReadyFile)} {
		if e := trusted(p, true); e != nil {
			return e
		}
	}
	if e := trusted(ReadyFile, false); e != nil {
		return fmt.Errorf("proxy egress rules not ready: %w", e)
	}
	want, e := boot()
	if e != nil {
		return e
	}
	got, e := os.ReadFile(ReadyFile)
	if e != nil {
		return e
	}
	if string(got) != string(want) {
		return fmt.Errorf("proxy egress readiness belongs to a different boot")
	}
	return nil
}
