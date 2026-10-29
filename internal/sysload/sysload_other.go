//go:build !linux

// readCPUTimes is unsupported off Linux; the controller falls back to throughput/latency signals only.

package sysload

// readCPUTimes is unsupported off Linux; the controller falls back to throughput/latency signals only.
func readCPUTimes() (idle, total uint64, ok bool) { return 0, 0, false }
