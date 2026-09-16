package main

import (
	"ctlvps/internal/proxysandbox"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "raw-probe" {
		return
	}
	runtime.LockOSThread()
	if e := proxysandbox.Install(); e != nil {
		panic(e)
	}
	if e := unix.Exec("/usr/bin/python3", []string{"python3", "/src/scripts/security-boundary-test/run.py", "probe", os.Args[2]}, os.Environ()); e != nil {
		panic(e)
	}
}
