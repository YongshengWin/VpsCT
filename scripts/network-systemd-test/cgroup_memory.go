//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func memoryPreflight() {
	run("systemd-run", "--unit=ctlvps-memory-preflight", "--property=MemoryMax=256M", "sleep", "30")
	defer run("systemctl", "stop", "ctlvps-memory-preflight.service")
	pid, err := strconv.Atoi(run("systemctl", "show", "ctlvps-memory-preflight.service", "--property=MainPID", "--value"))
	must(err)
	membership, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	must(err)
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	must(err)
	path, legacy, err := memoryCgroupPath(string(membership), string(mounts))
	if err != nil {
		panic(fmt.Sprintf("memory preflight: %v; membership=%s mounts=%s", err, membership, mounts))
	}
	file := "memory.max"
	if legacy {
		file = "memory.limit_in_bytes"
	}
	value, err := os.ReadFile(filepath.Join(path, file))
	if err != nil {
		panic(fmt.Sprintf("memory preflight: %v; membership=%s mounts=%s", err, membership, mounts))
	}
	if strings.TrimSpace(string(value)) != "268435456" {
		panic("memory preflight kernel limit mismatch: " + string(value))
	}
	fmt.Printf("PASS memory preflight: legacy=%v %s/%s=%s", legacy, path, file, value)
}

// Hybrid hosts can expose a unified hierarchy without its memory controller.
// Resolve the memory hierarchy and its mount root instead of assuming 0:: is it.
func memoryCgroupPath(membership, mounts string) (string, bool, error) {
	for _, legacy := range []bool{true, false} {
		for _, line := range strings.Split(membership, "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 || (legacy && !hasController(parts[1], "memory")) || (!legacy && parts[0] != "0") {
				continue
			}
			for _, mount := range strings.Split(mounts, "\n") {
				left, right, ok := strings.Cut(mount, " - ")
				fields, fs := strings.Fields(left), strings.Fields(right)
				if !ok || len(fields) < 5 || len(fs) < 3 {
					continue
				}
				if legacy && (fs[0] != "cgroup" || !hasController(fs[2], "memory")) || !legacy && fs[0] != "cgroup2" {
					continue
				}
				root, target := filepath.Clean(fields[3]), filepath.Clean(parts[2])
				rel, err := filepath.Rel(root, target)
				if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
					continue
				}
				return filepath.Join(fields[4], rel), legacy, nil
			}
		}
	}
	return "", false, fmt.Errorf("no mounted memory hierarchy for proxy")
}

func hasController(list, name string) bool {
	for _, item := range strings.Split(list, ",") {
		if item == name {
			return true
		}
	}
	return false
}
