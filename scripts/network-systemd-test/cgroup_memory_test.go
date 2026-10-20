//go:build linux

package main

import "testing"

func TestMemoryHierarchyResolution(t *testing.T) {
	for _, tc := range []struct {
		name, membership, mounts, want string
		legacy                         bool
	}{
		{"unified", "0::/proxy/service", "1 0 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup rw", "/sys/fs/cgroup/proxy/service", false},
		{"hybrid", "0::/proxy/service\n8:memory:/proxy/service", "1 0 0:1 / /sys/fs/cgroup/unified rw - cgroup2 cgroup rw\n2 0 0:2 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory", "/sys/fs/cgroup/memory/proxy/service", true},
		{"mount-root", "0::/docker/container/proxy", "1 0 0:1 /docker/container /sys/fs/cgroup rw - cgroup2 cgroup rw", "/sys/fs/cgroup/proxy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, legacy, err := memoryCgroupPath(tc.membership, tc.mounts)
			if err != nil || got != tc.want || legacy != tc.legacy {
				t.Fatalf("got %q legacy=%v err=%v", got, legacy, err)
			}
		})
	}
	if _, _, err := memoryCgroupPath("0::/outside", "1 0 0:1 /other /sys/fs/cgroup rw - cgroup2 cgroup rw"); err == nil {
		t.Fatal("accepted inaccessible hierarchy")
	}
}
