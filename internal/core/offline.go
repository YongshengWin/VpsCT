package core

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"ctlvps/internal/safehttp"
)

const offlineCoreDir = "/var/lib/ctlvps-agent/core-cache"

// A locally prepared cache opts this host into offline sing-box installs.
// No desired state can choose this path, inject an archive name or enable it.
func copyOfflineSingBox(dir, filename, pinnedSHA string, dst io.Writer) (present bool, checksum string, err error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return false, "", nil
	}
	if err != nil {
		return true, "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ownedByInstaller(info) {
		return true, "", errors.New("离线内核目录须由本机管理员拥有且权限为 0700")
	}
	if filepath.Base(filename) != filename || filename == "." || filename == ".." {
		return true, "", errors.New("离线内核文件名无效")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return true, "", err
	}
	defer root.Close()
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		return true, "", errors.New("离线内核目录在读取时已变化")
	}
	open := func(name string) (*os.File, error) {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("等待离线内核文件 %s", name)
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ownedByInstaller(info) {
			return nil, errors.New("离线内核文件必须为管理员持有的普通文件")
		}
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		actual, err := f.Stat()
		if err != nil || !os.SameFile(info, actual) {
			f.Close()
			return nil, errors.New("离线内核文件在读取时已变化")
		}
		return f, nil
	}
	manifest, err := open(filename + ".sha256")
	if err != nil {
		return true, "", err
	}
	raw, err := safehttp.ReadBounded(manifest, 1024)
	manifest.Close()
	if err != nil {
		return true, "", err
	}
	checksum = strings.TrimSpace(string(raw))
	if _, err := hex.DecodeString(checksum); err != nil || len(checksum) != 64 {
		return true, "", errors.New("离线内核 SHA256 文件必须只包含一个 64 位十六进制校验值")
	}
	if pinnedSHA != "" && !strings.EqualFold(pinnedSHA, checksum) {
		return true, "", errors.New("离线内核 SHA256 与锁定版本不一致")
	}
	archive, err := open(filename)
	if err != nil {
		return true, "", err
	}
	defer archive.Close()
	_, err = safehttp.CopyBounded(dst, archive, 200<<20)
	return true, checksum, err
}

func ownedByInstaller(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
