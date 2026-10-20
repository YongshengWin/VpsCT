//go:build linux

package agentnet

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var identityMu sync.Mutex

func trustedNologin(path string) bool {
	if path != "/usr/sbin/nologin" && path != "/sbin/nologin" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for current, first := resolved, true; current != "/"; current, first = filepath.Dir(current), false {
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || info.Mode().Perm()&0022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if first {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return false
			}
		} else if !info.IsDir() {
			return false
		}
	}
	return true
}

func nologinShell() (string, error) {
	for _, path := range []string{"/usr/sbin/nologin", "/sbin/nologin"} {
		if trustedNologin(path) {
			return path, nil
		}
	}
	return "", errors.New("缺少可信的 nologin 系统程序")
}

// Use a dedicated identity instead of nobody, which is shared by unrelated
// daemons. This fixed local account is provisioned before any untrusted parsing.
func networkIdentity() (uint32, uint32, error) {
	identityMu.Lock()
	defer identityMu.Unlock()
	const name = "ctlvps-net"
	u, e := user.Lookup(name)
	if _, missing := e.(user.UnknownUserError); missing && os.Geteuid() == 0 {
		shell, err := nologinShell()
		if err != nil {
			return 0, 0, err
		}
		cmd := exec.Command("/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", shell, "--comment", "VpsCT isolated network worker", name)
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
		// A concurrent first installation may already have created the account.
		_ = cmd.Run()
		u, e = user.Lookup(name)
	}
	if e != nil {
		return 0, 0, errors.New("无法解析专用 ctlvps-net 系统用户")
	}
	uid, e := strconv.ParseUint(u.Uid, 10, 32)
	if e != nil {
		return 0, 0, e
	}
	gid, e := strconv.ParseUint(u.Gid, 10, 32)
	if e != nil {
		return 0, 0, e
	}
	if uid < 100 || uid == 65534 || gid == 0 || u.HomeDir != "/nonexistent" || u.Name != "VpsCT isolated network worker" {
		return 0, 0, errors.New("ctlvps-net 系统用户冲突，拒绝复用")
	}
	group, e := user.LookupGroupId(u.Gid)
	if e != nil || group.Name != name {
		return 0, 0, errors.New("ctlvps-net 必须使用独立系统组")
	}
	groupFile, e := os.ReadFile("/etc/group")
	if e != nil {
		return 0, 0, e
	}
	for _, line := range strings.Split(string(groupFile), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 || fields[2] != u.Gid {
			continue
		}
		if fields[0] != name || (fields[3] != "" && fields[3] != name) {
			return 0, 0, errors.New("ctlvps-net 组包含其他账户")
		}
	}
	// Do not accept another passwd identity sharing this UID or an interactive shell.
	passwd, e := os.ReadFile("/etc/passwd")
	if e != nil {
		return 0, 0, e
	}
	count := 0
	for _, line := range strings.Split(string(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 7 && fields[3] == u.Gid && fields[0] != name {
			return 0, 0, errors.New("ctlvps-net 组被其他账户复用")
		}
		if len(fields) != 7 || fields[2] != u.Uid {
			continue
		}
		count++
		if fields[0] != name || !trustedNologin(fields[6]) {
			return 0, 0, errors.New("ctlvps-net 身份不独占或允许交互登录")
		}
	}
	if count != 1 {
		return 0, 0, errors.New("ctlvps-net 必须是独立本地系统用户")
	}
	return uint32(uid), uint32(gid), nil
}
func isolate(c *exec.Cmd) error {
	uid, gid, e := networkIdentity()
	if e != nil {
		return e
	}
	c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}, Pdeathsig: syscall.SIGKILL}
	return nil
}
