//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"ctlvps/internal/maintenance"
	"ctlvps/internal/openrc"
)

// Replace the actual fixture launcher before the client starts. The production
// refresh path must recognize old executable inodes and restore only the public
// service which was running, without enrolling or contacting a controller.
func verifyHelperRefresh(ctx context.Context) {
	const launcher = "/usr/local/bin/ctlvps-agent"
	const receipt = "/run/ctlvps-openrc/refresh.json"
	const public = "ctlvps-singbox.service"
	const private = "ctlvps-singbox-private.service"
	helpers := []string{"ctlvps-service-host.service", "ctlvps-proxy-guard.service"}
	before := map[string]int{}
	for _, unit := range helpers {
		pid, err := openrc.MainPID(unit)
		must(err)
		if pid == 0 {
			panic("refresh test requires a running helper: " + unit)
		}
		before[unit] = pid
	}
	if pid, err := openrc.MainPID(public); err != nil || pid == 0 {
		panic("refresh test requires a running public proxy")
	}
	// Define a real fixed private unit, then explicitly keep it stopped and
	// disabled. It must remain absent from the protected restart receipt.
	publicBody, err := os.ReadFile(openrc.UnitDir + "/" + public)
	must(err)
	privateBody := strings.ReplaceAll(string(publicBody), "public", "private")
	privateBody = strings.ReplaceAll(privateBody, "ctlvps-sb", "ctlvps-sp")
	privateBody = strings.ReplaceAll(privateBody, "/var/log/ctlvps/sing-box.log", "/var/log/ctlvps/sing-box-private.log")
	_, err = openrc.InstallUnit(private, privateBody)
	must(err)
	_, err = openrc.Control(ctx, "stop", private)
	must(err)
	_, err = openrc.Control(ctx, "disable", private)
	must(err)
	assertPrivateDisabled := func() {
		if pid, err := openrc.MainPID(private); err != nil || pid != 0 {
			panic("helper refresh started a stopped private proxy")
		}
		if _, err := os.Lstat("/etc/runlevels/default/ctlvps-singbox-private"); !os.IsNotExist(err) {
			panic("helper refresh created a private runlevel link")
		}
		if _, err := openrc.Control(ctx, "is-enabled", private); err == nil {
			panic("helper refresh enabled a disabled private proxy")
		}
	}
	assertPrivateDisabled()
	release, err := maintenance.NewManager().WaitConfigurationLock(ctx)
	must(err)
	defer release()
	oldLauncher, err := os.Stat(launcher)
	must(err)
	src, err := os.Open(launcher)
	must(err)
	defer src.Close()
	dst, err := os.CreateTemp("/usr/local/bin", ".fixture-agent-refresh-")
	must(err)
	defer os.Remove(dst.Name())
	must(dst.Chmod(0755))
	_, err = io.Copy(dst, src)
	must(err)
	must(dst.Sync())
	must(dst.Close())
	must(os.Rename(dst.Name(), launcher))
	dir, err := os.Open("/usr/local/bin")
	must(err)
	must(dir.Sync())
	must(dir.Close())
	newLauncher, err := os.Stat(launcher)
	must(err)
	if os.SameFile(oldLauncher, newLauncher) {
		panic("atomic launcher replacement did not change its inode")
	}
	for unit, pid := range before {
		running, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
		must(err)
		if !os.SameFile(oldLauncher, running) || os.SameFile(newLauncher, running) {
			panic("refresh setup did not retain the old helper inode: " + unit)
		}
	}
	must(openrc.RefreshHelpers(ctx))
	assertRefreshed := func(requireNewPID bool) {
		current, err := os.Stat(launcher)
		must(err)
		for _, unit := range helpers {
			pid, err := openrc.MainPID(unit)
			must(err)
			if pid == 0 || (requireNewPID && pid == before[unit]) {
				panic("helper refresh retained its old process: " + unit)
			}
			running, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
			must(err)
			if !os.SameFile(current, running) {
				panic("helper refresh retained its old executable inode: " + unit)
			}
		}
		if pid, err := openrc.MainPID(public); err != nil || pid == 0 {
			panic("helper refresh did not resume its public proxy")
		}
		must(openrc.VerifyProxy(public, launcher+" proxy-exec singbox run public"))
		assertPrivateDisabled()
		if _, err := os.Stat(receipt); !os.IsNotExist(err) {
			panic("completed helper refresh kept its resume receipt")
		}
	}
	assertRefreshed(true)
	// Reproduce interruption after helper replacement: the receipt is present
	// and the old public process is stopped, while both helpers are current.
	// Resume must not be skipped just because the inode comparison now succeeds.
	writeJSON(receipt, map[string]any{"version": 1, "units": []string{public}})
	_, err = openrc.Control(ctx, "stop", public)
	must(err)
	if pid, err := openrc.MainPID(public); err != nil || pid != 0 {
		panic("receipt resume setup did not stop the public proxy")
	}
	must(openrc.RefreshHelpers(ctx))
	assertRefreshed(false)
}
