// Package device classifies the physical device backing a path (rotational HDD vs
// SSD), reports its optimal I/O size and a stable id for same-spindle detection,
// and samples its %utilization over time. These feed the controller's prior (seed
// concurrency, pick the trusted signal) and its HDD %util growth guard.
//
// Only Linux has a full implementation (via /sys and /proc/diskstats); other
// platforms return Unknown/unsupported and the controller relies on the portable
// throughput and latency signals.
package device

import "time"

// Class is the storage medium class of a device.
type Class int

const (
	Unknown Class = iota
	SSD
	HDD
)

func (c Class) String() string {
	switch c {
	case SSD:
		return "SSD"
	case HDD:
		return "HDD"
	default:
		return "unknown"
	}
}

// Info describes the physical device backing a path. Zero values mean "unknown".
type Info struct {
	Class         Class
	OptimalIOSize int64  // bytes; 0 if unknown
	DeviceID      string // stable id of the physical device (same-spindle detection)
	Name          string // kernel name, e.g. "vda" or "nvme0n1"; "" if unknown
}

// Rotational reports whether the device is a spinning disk (seek-sensitive).
func (i Info) Rotational() bool { return i.Class == HDD }

// Lookup classifies the device backing path. It never errors; on any failure it
// returns a zero Info (Class == Unknown).
func Lookup(path string) Info { return lookup(path) }

// UtilSampler reads a device's busy-% between successive Sample calls. A sampler
// with an empty device name (or on an unsupported platform) always reports
// ok=false.
type UtilSampler struct {
	name      string
	prevTicks uint64
	prevTime  time.Time
	primed    bool
}

// NewUtilSampler returns a sampler for the named device (use Info.Name).
func NewUtilSampler(name string) *UtilSampler { return &UtilSampler{name: name} }

var readIOTicksForSample = readIOTicks

// Sample returns the device %util [0,100] since the previous call. The first call
// primes the baseline and returns ok=false, as does any call when the device is
// unknown or unsupported.
func (u *UtilSampler) Sample() (utilPct float64, ok bool) {
	if u.name == "" {
		return 0, false
	}
	ticks, ok := readIOTicksForSample(u.name)
	if !ok {
		return 0, false
	}
	now := time.Now()
	if !u.primed {
		u.prevTicks, u.prevTime, u.primed = ticks, now, true
		return 0, false
	}
	dtMs := float64(now.Sub(u.prevTime).Milliseconds())
	dTicks := float64(ticks - u.prevTicks)
	u.prevTicks, u.prevTime = ticks, now
	if dtMs <= 0 {
		return 0, false
	}
	utilPct = dTicks / dtMs * 100
	if utilPct < 0 {
		utilPct = 0
	}
	if utilPct > 100 {
		utilPct = 100
	}
	return utilPct, true
}
