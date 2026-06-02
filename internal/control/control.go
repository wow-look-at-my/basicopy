// Package control implements basicopy's auto-scaling controller: the closed loop
// that decides how many copy streams (the worker gate's limit W) should run.
//
// The controller is deliberately pure decision logic with no I/O, so it can be
// unit-tested against synthetic throughput curves. The engine feeds it a Sample
// per tick (observed throughput plus optional guard signals) and applies the
// returned W to the gate.
//
// Design (see the plan): the *throughput plateau* is the primary signal because
// it is the only one present in every bottleneck — disk, bus/link, or CPU-bound
// security software all manifest as "more workers stop raising throughput". The
// controller climbs W while smoothed throughput keeps rising, then converges to
// the minimum W that sustains the plateau (so a saturated USB bus isn't served by
// 64 idle-ish streams). System-CPU and HDD %util act only as guards that forbid
// growing — they never override a clear plateau.
package control

type phase int

const (
	climbing phase = iota
	holding
	trimming
)

// Sample is one observation at a control tick. Unknown guard signals are encoded
// as negative values so they are simply ignored.
type Sample struct {
	Throughput float64 // bytes/sec observed since the last tick
	SysCPU     float64 // system-wide CPU busy fraction [0,1]; <0 if unknown
	DevUtil    float64 // device %util [0,100]; <0 if unknown
	Rotational bool    // is a rotational (HDD) device involved?
}

// Controller decides the worker limit W from a stream of Samples.
type Controller struct {
	min, max int
	w        int

	ema    float64
	emaSet bool
	refT   float64 // throughput at the W we last grew from (climb comparison)
	peakT  float64 // best plateau throughput seen (trim comparison)
	prevW  int     // W before the current tentative change
	ph     phase
	dwell  int // ticks to wait after a change for throughput to stabilize
	hold   int // ticks spent holding since the last probe
	probeUp bool

	// Tunables (exported for testing/override).
	Alpha    float64 // EWMA smoothing factor for throughput
	Margin   float64 // fractional throughput change considered significant
	DwellN   int     // ticks to dwell after changing W
	Reprobe  int     // holding ticks between re-probes
	CPUCeil  float64 // system CPU fraction above which we won't grow
	UtilCeil float64 // HDD %util above which we won't grow
}

// New creates a controller bounded to [min,max], starting at start.
func New(min, max, start int) *Controller {
	if min < 1 {
		min = 1
	}
	if max < min {
		max = min
	}
	if start < min {
		start = min
	}
	if start > max {
		start = max
	}
	return &Controller{
		min: min, max: max, w: start, prevW: start,
		ph:      climbing,
		Alpha:   0.35,
		Margin:  0.06,
		DwellN:  2,
		Reprobe: 8,
		CPUCeil: 0.95,
		UtilCeil: 95,
	}
}

// W returns the current worker limit.
func (c *Controller) W() int { return c.w }

func (c *Controller) canGrow(s Sample) bool {
	if s.SysCPU >= 0 && s.SysCPU > c.CPUCeil {
		return false
	}
	if s.Rotational && s.DevUtil >= 0 && s.DevUtil > c.UtilCeil {
		return false
	}
	return true
}

// grow returns the next (larger) W using ~1.5x multiplicative growth so the loop
// converges quickly even when the answer is large.
func (c *Controller) grow() int {
	n := c.w + c.w/2
	if n <= c.w {
		n = c.w + 1
	}
	if n > c.max {
		n = c.max
	}
	return n
}

// Update folds in one Sample and returns the (possibly changed) worker limit.
func (c *Controller) Update(s Sample) int {
	if !c.emaSet {
		c.ema = s.Throughput
		c.emaSet = true
	} else {
		c.ema = c.Alpha*s.Throughput + (1-c.Alpha)*c.ema
	}

	// Wait for throughput to stabilize after the most recent change.
	if c.dwell > 0 {
		c.dwell--
		return c.w
	}

	switch c.ph {
	case climbing:
		if c.ema > c.refT*(1+c.Margin) {
			// The last increase paid off; keep climbing if allowed.
			c.refT = c.ema
			if c.ema > c.peakT {
				c.peakT = c.ema
			}
			if c.canGrow(s) && c.w < c.max {
				c.prevW = c.w
				c.w = c.grow()
				c.dwell = c.DwellN
			} else {
				c.ph = holding
				c.hold = 0
			}
		} else {
			// The last increase didn't help: revert to the knee and hold.
			c.w = c.prevW
			c.ph = holding
			c.hold = 0
			c.dwell = c.DwellN
		}

	case holding:
		c.hold++
		c.refT = c.ema
		if c.ema > c.peakT {
			c.peakT = c.ema
		}
		if c.hold >= c.Reprobe {
			c.hold = 0
			c.probeUp = !c.probeUp
			switch {
			case c.probeUp && c.canGrow(s) && c.w < c.max:
				c.prevW = c.w
				c.w = c.grow()
				c.ph = climbing
				c.dwell = c.DwellN
			case c.w > c.min:
				// Trim toward the minimum W that still sustains the plateau.
				c.prevW = c.w
				c.w--
				c.ph = trimming
				c.dwell = c.DwellN
			case c.canGrow(s) && c.w < c.max:
				c.prevW = c.w
				c.w = c.grow()
				c.ph = climbing
				c.dwell = c.DwellN
			}
		}

	case trimming:
		if c.ema >= c.peakT*(1-c.Margin) {
			// Removing a worker kept throughput within margin of the plateau
			// peak: keep the lower W and keep looking for the minimum.
			c.refT = c.ema
			c.ph = holding
			c.hold = 0
		} else {
			// It cost throughput: restore and hold.
			c.w = c.prevW
			c.ph = holding
			c.hold = 0
			c.dwell = c.DwellN
		}
	}

	if c.w < c.min {
		c.w = c.min
	}
	if c.w > c.max {
		c.w = c.max
	}
	return c.w
}
