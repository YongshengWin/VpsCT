//go:build linux

package agentnet

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var identityMu sync.Mutex

// Use a dedicated identity instead of nobody, which is shared by unrelated
// daemons. This fixed local account is provisioned before any untrusted parsing.
func networkIdentity() (uint32, uint32, error) {
	identityMu.Lock()
	defer identityMu.Unlock()
	const name = "ctlvps-net"
	u, e := user.Lookup(name)
	if _, missing := e.(user.UnknownUserError); missing && os.Geteuid() == 0 {
		cmd := exec.Command("/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "VpsCT isolated network worker", name)
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
		if fields[0] != name || fields[6] != "/usr/sbin/nologin" {
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
