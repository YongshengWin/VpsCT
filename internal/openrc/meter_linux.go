//go:build linux

package openrc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const meterRoot = "/sys/fs/bpf/ctlvps-meters"

type bpfInsn struct {
	Code, Reg uint8
	Off       int16
	Imm       int32
}

// These tiny cgroup-skb programs only count bytes and always allow traffic.
// Atomic additions preserve totals across concurrent CPUs. Each direction has
// its own map slot; the pinned map outlives proxy and agent restarts.
func meterProgram(fd, direction int) []bpfInsn {
	return []bpfInsn{
		{0x61, 0x16, 0, 0},                      // r6 = skb->len
		{0x62, 0x0a, -4, int32(direction)},      // stack key
		{0xbf, 0xa2, 0, 0}, {0x07, 0x02, 0, -4}, // r2 = &key
		{0x18, 0x11, 0, int32(fd)}, {}, // r1 = map fd
		{0x85, 0, 0, 1},    // map_lookup_elem
		{0x15, 0, 1, 0},    // skip atomic add on NULL
		{0xdb, 0x60, 0, 0}, // lock *(u64 *)r0 += r6
		{0xb7, 0x00, 0, 1}, {0x95, 0, 0, 0},
	}
}

func bpfCall(cmd int, attr unsafe.Pointer, size uintptr) (int, error) {
	r, _, e := unix.Syscall(unix.SYS_BPF, uintptr(cmd), uintptr(attr), size)
	if e != 0 {
		return -1, fmt.Errorf("OpenRC cgroup accounting BPF operation %d: %w", cmd, e)
	}
	return int(r), nil
}

func bpfObject(path string, fd int, pin bool) (int, error) {
	name, err := unix.BytePtrFromString(path)
	if err != nil {
		return -1, err
	}
	a := struct {
		Path      uint64
		FD, Flags uint32
	}{uint64(uintptr(unsafe.Pointer(name))), uint32(fd), 0}
	op := unix.BPF_OBJ_GET
	if pin {
		op = unix.BPF_OBJ_PIN
	}
	n, err := bpfCall(op, unsafe.Pointer(&a), unsafe.Sizeof(a))
	runtime.KeepAlive(name)
	return n, err
}

func meterDirectory(unit string) (string, error) {
	id, ok := meterID(unit)
	if !ok {
		return "", errors.New("invalid OpenRC meter identity")
	}
	return filepath.Join(meterRoot, "n"+id), nil
}

func EnsureMeter(unit string) error {
	dir, err := meterDirectory(unit)
	if err != nil {
		return err
	}
	var st unix.Statfs_t
	if err = unix.Statfs("/sys/fs/bpf", &st); err != nil {
		return err
	}
	if st.Type != unix.BPF_FS_MAGIC {
		if err = trustedDirectory("/sys/fs/bpf", false); err != nil {
			return err
		}
		entries, err := os.ReadDir("/sys/fs/bpf")
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("unmounted bpffs directory is not empty")
		}
		if err = unix.Mount("bpffs", "/sys/fs/bpf", "bpf", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return fmt.Errorf("mieru requires bpffs: %w", err)
		}
	}
	if err = trustedMeterDirectory(dir); err != nil {
		return err
	}
	if err = os.Chmod(meterRoot, 0700); err != nil {
		return err
	}
	// Serialize creation across service-host and agent processes.
	lock, err := unix.Open("/run/ctlvps-openrc-meter.lock", unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(lock)
	if err = unix.Flock(lock, unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(lock, unix.LOCK_UN)
	g, _ := cgroup(unit)
	if err = ensureSlice("ctlvps-proxy.slice"); err != nil {
		return err
	}
	if err = makeGroup(g); err != nil {
		return err
	}
	fd, err := bpfObject(dir+"/map", 0, false)
	if err != nil {
		if !errors.Is(err, unix.ENOENT) {
			return err
		}
		a := struct{ Type, Key, Value, Count, Flags uint32 }{unix.BPF_MAP_TYPE_ARRAY, 4, 8, 2, 0}
		fd, err = bpfCall(unix.BPF_MAP_CREATE, unsafe.Pointer(&a), unsafe.Sizeof(a))
		if err != nil {
			return err
		}
		if _, err = bpfObject(dir+"/map", fd, true); err != nil {
			unix.Close(fd)
			return err
		}
	}
	defer unix.Close(fd)
	cg, err := unix.Open(cgpath(g), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(cg)
	for direction, name := range []string{"rx", "tx"} {
		link, err := bpfObject(dir+"/"+name, 0, false)
		if err == nil {
			err = verifyMeterLink(link, g, direction)
			unix.Close(link)
			if err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, unix.ENOENT) {
			return err
		}
		insns := meterProgram(fd, direction)
		license := []byte("GPL\x00")
		log := make([]byte, 16<<10)
		a := struct {
			Type, Count       uint32
			Insns, License    uint64
			LogLevel, LogSize uint32
			Log               uint64
			Kernel, Flags     uint32
			Name              [16]byte
			IfIndex, Attach   uint32
		}{
			Type: unix.BPF_PROG_TYPE_CGROUP_SKB, Count: uint32(len(insns)), Insns: uint64(uintptr(unsafe.Pointer(&insns[0]))), License: uint64(uintptr(unsafe.Pointer(&license[0]))), LogLevel: 1, LogSize: uint32(len(log)), Log: uint64(uintptr(unsafe.Pointer(&log[0]))), Attach: uint32(direction),
		}
		prog, err := bpfCall(unix.BPF_PROG_LOAD, unsafe.Pointer(&a), unsafe.Sizeof(a))
		runtime.KeepAlive(insns)
		runtime.KeepAlive(license)
		runtime.KeepAlive(log)
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimRight(string(log), "\x00"))
		}
		attach := struct{ Prog, Target, Type, Flags uint32 }{uint32(prog), uint32(cg), uint32(direction), 0}
		link, err = bpfCall(unix.BPF_LINK_CREATE, unsafe.Pointer(&attach), unsafe.Sizeof(attach))
		unix.Close(prog)
		if err != nil {
			return err
		}
		_, err = bpfObject(dir+"/"+name, link, true)
		unix.Close(link)
		if err != nil {
			return err
		}
	}
	return nil
}

func ReadMeter(unit string) (rx, tx int64, epoch string, err error) {
	dir, err := meterDirectory(unit)
	if err != nil {
		return 0, 0, "", err
	}
	// An incomplete attachment must never be treated as a complete reading.
	for direction, name := range []string{"rx", "tx"} {
		fd, e := bpfObject(dir+"/"+name, 0, false)
		if e != nil {
			return 0, 0, "", e
		}
		g, _ := cgroup(unit)
		e = verifyMeterLink(fd, g, direction)
		unix.Close(fd)
		if e != nil {
			return 0, 0, "", e
		}
	}
	fd, err := bpfObject(dir+"/map", 0, false)
	if err != nil {
		return 0, 0, "", err
	}
	defer unix.Close(fd)
	values := [2]uint64{}
	for key := uint32(0); key < 2; key++ {
		a := struct {
			FD, Pad           uint32
			Key, Value, Flags uint64
		}{FD: uint32(fd), Key: uint64(uintptr(unsafe.Pointer(&key))), Value: uint64(uintptr(unsafe.Pointer(&values[key])))}
		_, err = bpfCall(unix.BPF_MAP_LOOKUP_ELEM, unsafe.Pointer(&a), unsafe.Sizeof(a))
		runtime.KeepAlive(&key)
		runtime.KeepAlive(&values)
		if err != nil {
			return 0, 0, "", err
		}
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return 0, 0, "", err
	}
	var stat unix.Stat_t
	if err = unix.Stat(dir+"/map", &stat); err != nil {
		return 0, 0, "", err
	}
	if values[0] > 1<<63-1 || values[1] > 1<<63-1 {
		return 0, 0, "", errors.New("meter overflow")
	}
	return int64(values[0]), int64(values[1]), fmt.Sprintf("%s:bpf:%d", strings.TrimSpace(string(boot)), stat.Ino), nil
}

// Called only after final settlement ACK and an empty parent cgroup.
func RemoveMeter(unit string) error {
	dir, err := meterDirectory(unit)
	if err != nil {
		return err
	}
	if populated(unit) {
		return errors.New("meter cgroup still populated")
	}
	for _, name := range []string{"rx", "tx", "map"} {
		if err = os.Remove(dir + "/" + name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err = os.Remove(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Empty service leaf directories may remain; their lifetime is harmless and
	// they cannot carry accounting after both pinned links have been removed.
	return nil
}

func RemoveAllMeters() error {
	if populated("ctlvps-proxy.slice") {
		return errors.New("proxy tree still populated")
	}
	entries, err := os.ReadDir(meterRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		unit := "ctlvps-proxy-" + entry.Name() + ".slice"
		if _, ok := meterID(unit); !ok {
			return errors.New("unknown pinned meter identity")
		}
		if err := RemoveMeter(unit); err != nil {
			return err
		}
	}
	return nil
}

// The standard bpffs root is commonly root-owned 01777. Its sticky bit
// protects our root-owned 0700 subtree; do not chmod a shared system mount.
func trustedMeterDirectory(dir string) error {
	if err := trustedDirectory("/sys/fs", false); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat("/sys/fs/bpf", &st); err != nil {
		return err
	}
	if st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0) {
		return errors.New("unsafe bpffs root")
	}
	for _, path := range []string{meterRoot, dir} {
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		if st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0077 != 0 {
			return errors.New("unsafe pinned meter directory")
		}
	}
	return nil
}

func verifyMeterLink(fd int, group string, direction int) error {
	info := struct {
		Type, ID, Program, Pad uint32
		Cgroup                 uint64
		Attach, Pad2           uint32
	}{}
	attr := struct {
		FD, Length uint32
		Info       uint64
	}{uint32(fd), uint32(unsafe.Sizeof(info)), uint64(uintptr(unsafe.Pointer(&info)))}
	_, err := bpfCall(unix.BPF_OBJ_GET_INFO_BY_FD, unsafe.Pointer(&attr), unsafe.Sizeof(attr))
	runtime.KeepAlive(&info)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Stat(cgpath(group), &st); err != nil {
		return err
	}
	if info.Type != unix.BPF_LINK_TYPE_CGROUP || info.Attach != uint32(direction) || info.Cgroup != st.Ino {
		return errors.New("pinned meter is not attached to the current node cgroup")
	}
	return nil
}
