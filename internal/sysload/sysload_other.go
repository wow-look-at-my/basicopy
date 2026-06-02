//go:build !linux

package sysload

// readCPUTimes is unsupported off Linux; the controller falls back to throughput
// and latency signals alone.
func readCPUTimes() (idle, total uint64, ok bool) { return 0, 0, false }
