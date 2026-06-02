//go:build !linux

package device

// lookup and readIOTicks are unsupported off Linux. Native classification
// (IOKit on macOS, StorageDeviceSeekPenaltyProperty on Windows) is layered on
// later; until then the controller uses throughput/latency signals only.
func lookup(path string) Info { return Info{} }

func readIOTicks(name string) (uint64, bool) { return 0, false }
