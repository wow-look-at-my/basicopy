//go:build linux

package sysload

import (
	"os"
	"strconv"
	"strings"
)

func readCPUTimes() (idle, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseCPULine(string(data))
}

// parseCPULine parses the aggregate "cpu" line of /proc/stat, returning idle time
// (idle + iowait) and the total of all time fields, in jiffies.
func parseCPULine(stat string) (idle, total uint64, ok bool) {
	for _, line := range strings.Split(stat, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line) // ["cpu", user, nice, system, idle, iowait, ...]
		if len(fields) < 6 {
			return 0, 0, false
		}
		var vals []uint64
		for _, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			vals = append(vals, v)
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3] + vals[4] // idle + iowait
		return idle, total, true
	}
	return 0, 0, false
}
