// Install this verifier and its independent trust policy through a trusted local channel.
package main

import (
	"ctlvps/internal/agentnet"
	"ctlvps/internal/secureupdate"
	"ctlvps/internal/store"
	"fmt"
	"os"
)

func main() {
	if ok, e := agentnet.Entry(os.Args[1:]); ok {
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	if ok, e := store.BackupEntry(os.Args[1:]); ok {
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	ok, e := secureupdate.Entry(os.Args[1:])
	if !ok {
		e = fmt.Errorf("usage: ctlvps-verify verify-release COMPONENT VERSION FILE")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
