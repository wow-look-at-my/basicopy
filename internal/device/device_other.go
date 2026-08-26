//go:build !linux

package device

// lookup is unsupported off Linux; the controller falls back to throughput/latency signals only.
func lookup(path string) Info { return Info{} }

func readIOTicks(name string) (uint64, bool) { return 0, false }
