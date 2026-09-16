// ctlvps-sign is an offline tool; its keys never belong on a controller or VPS.
package main

import (
	"ctlvps/internal/secureupdate"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	mode := flag.String("mode", "publish", "init, rotate or publish")
	previous := flag.String("previous-keys", "", "old offline keys for root rotation")
	keys := flag.String("keys", "", "private signing key directory (offline)")
	out := flag.String("out", "", "new metadata directory")
	manifest := flag.String("manifest", "", "JSON release file manifest")
	version := flag.Int64("metadata-version", 0, "monotonically increasing metadata version")
	flag.Parse()
	var err error
	if *keys == "" {
		err = fmt.Errorf("--keys required")
	} else if *mode == "init" {
		err = secureupdate.InitRepository(*keys)
	} else if *mode == "rotate" {
		err = secureupdate.RotateRoot(*previous, *keys)
	} else if *mode == "publish" {
		if _, err = os.Stat(*keys + "/security-policy.json"); err != nil {
			fmt.Fprintln(os.Stderr, "publish requires an explicit cumulative security-policy.json in --keys")
			os.Exit(1)
		}
		var files []secureupdate.ReleaseFile
		var b []byte
		b, err = os.ReadFile(*manifest)
		if err == nil {
			err = json.Unmarshal(b, &files)
		}
		if err == nil {
			err = secureupdate.PublishRepository(*keys, *out, files, *version, time.Now().Add(30*24*time.Hour))
		}
	} else {
		err = fmt.Errorf("unknown mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
