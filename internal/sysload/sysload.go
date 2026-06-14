// Package sysload samples system-wide CPU utilization so the controller can avoid
// pegging the machine — e.g. when CPU-intensive on-access security software makes
// the copy CPU-bound rather than I/O-bound. On platforms where it can't read the
// figure, Sample reports ok=false and the controller simply ignores the signal.
package sysload

// Sampler tracks system-wide CPU busy time across calls.
type Sampler struct {
	prevIdle  uint64
	prevTotal uint64
	primed    bool
}

// New returns a fresh Sampler. The first Sample call primes the baseline.
func New() *Sampler { return &Sampler{} }

var readCPUTimesForSample = readCPUTimes

// Sample returns the busy fraction [0,1] of total CPU since the previous call.
// The first call (and any call on an unsupported platform) returns ok=false.
func (s *Sampler) Sample() (busy float64, ok bool) {
	idle, total, ok := readCPUTimesForSample()
	if !ok {
		return 0, false
	}
	if !s.primed {
		s.prevIdle, s.prevTotal, s.primed = idle, total, true
		return 0, false
	}
	dTotal := total - s.prevTotal
	dIdle := idle - s.prevIdle
	s.prevIdle, s.prevTotal = idle, total
	if dTotal == 0 {
		return 0, false
	}
	busy = 1 - float64(dIdle)/float64(dTotal)
	if busy < 0 {
		busy = 0
	}
	if busy > 1 {
		busy = 1
	}
	return busy, true
}
