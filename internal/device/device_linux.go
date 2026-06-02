//go:build linux

package device

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func lookup(path string) Info {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		// The path may not exist yet (a destination); classify its parent.
		if err := unix.Stat(filepath.Dir(path), &st); err != nil {
			return Info{}
		}
	}
	diskDir, ok := resolveDiskDir(unix.Major(st.Dev), unix.Minor(st.Dev))
	if !ok {
		return Info{}
	}

	info := Info{Name: filepath.Base(diskDir), DeviceID: diskDir}
	if rot, ok := readSysUint(filepath.Join(diskDir, "queue", "rotational")); ok {
		if rot == 1 {
			info.Class = HDD
		} else {
			info.Class = SSD
		}
	}
	if ois, ok := readSysUint(filepath.Join(diskDir, "queue", "optimal_io_size")); ok {
		info.OptimalIOSize = int64(ois)
	}
	return info
}

// resolveDiskDir maps a device number to its whole-disk sysfs directory, walking
// up from a partition to its parent disk (where queue/ lives).
func resolveDiskDir(major, minor uint32) (string, bool) {
	link := fmt.Sprintf("/sys/dev/block/%d:%d", major, minor)
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(real, "partition")); err == nil {
		real = filepath.Dir(real) // a partition -> its parent disk
	}
	if _, err := os.Stat(filepath.Join(real, "queue")); err != nil {
		return "", false
	}
	return real, true
}

func readSysUint(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func readIOTicks(name string) (uint64, bool) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, false
	}
	return parseIOTicks(string(data), name)
}

// parseIOTicks extracts the io_ticks field (ms spent doing I/O) for the named
// device from /proc/diskstats. Field layout per line: major minor name then 11+
// stats; io_ticks is the 10th stat (0-based index 12 overall).
func parseIOTicks(data, name string) (uint64, bool) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 13 || f[2] != name {
			continue
		}
		v, err := strconv.ParseUint(f[12], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
