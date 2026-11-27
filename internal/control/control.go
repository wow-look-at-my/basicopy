// Package control implements basicopy's auto-scaling controller: the closed loop
// that decides how many copy streams (the worker gate's limit W) should run.
//
// The controller is deliberately pure decision logic with no I/O, so it can be
// unit-tested against synthetic throughput curves. The engine feeds it a Sample
// per tick (observed throughput plus optional guard signals) and applies the
// returned W to the gate.
//
// Design (see the plan): the *throughput plateau* is the primary signal because
// it is the a single present in every bottleneck. Disk, bus/link, or CPU-bound
// security software all manifest as "more workers stop raising throughput".
// System-CPU and HDD %util act only as guards that forbid growing — they never
// override a clear plateau.
package control

type phase int

const (
	climbing phase = iota
	holding
	trimming
)

// Sample is a single observation at a control tick. Unknown guard signals are
// encoded as negative values so they are ignored.
type Sample struct {
	Throughput float64 // work per second since the last tick
	SysCPU     float64
	DevUtil    float64
	Rotational bool // is a rotational (HDD) device involved?

	// Blocked reports every stream busy with empty throughput: storage, not W, is the limit.
	Blocked bool

	// Unsettled reports that the copy does not run W streams now, so this throughput does not measure W.
	Unsettled bool
}

// Controller decides the worker limit W from a stream of Samples.
type Controller struct {
	min, max int
	w        int
	prevW    int // W before the last change
	ph       phase
	dwell    int // ticks to skip after a change

	// The window in progress, and the window before it at this W.
	sum     float64
	n       int
	last    float64
	hasLast bool

	refT   float64
	hasRef bool

	hold    int // ticks held since the last probe
	holdSum float64
	holdN   int
	probeUp bool

	trimRef  float64 // throughput the current run of trims must keep
	trimStep int

	// Tunables (exported for testing/override).
	Margin   float64 // fractional throughput change considered significant
	DwellN   int     // ticks to dwell after changing W
	WindowN  int     // ticks averaged into one measurement
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
		ph:       climbing,
		Margin:   0.06,
		DwellN:   2,
		WindowN:  3,
		Reprobe:  8,
		CPUCeil:  0.95,
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

// Update folds in a single Sample and returns the (possibly changed) worker limit.
func (c *Controller) Update(s Sample) int {
	// The streams still start or end. A tick counts only when W of them run.
	if s.Unsettled {
		c.resetWindow()
		return c.w
	}

	// Wait for throughput to stabilize after the most recent change.
	if c.dwell > 0 {
		c.dwell--
		return c.w
	}

	// Handle earliest: the plateau test below misreads a stall as "no gain" and reverts W forever.
	if s.Blocked {
		if c.canGrow(s) && c.w < c.max {
			c.set(c.grow())
			// The next probe after the stall is a trim.
			c.toHold()
			c.probeUp = true
		}
		c.resetWindow()
		return c.w
	}

	c.sum += s.Throughput
	c.n++
	if c.n < c.WindowN {
		return c.w
	}
	m := c.sum / float64(c.n)
	c.sum, c.n = 0, 0

	switch c.ph {
	case climbing:
		c.climb(m, s)
	case holding:
		c.held(m, s)
	case trimming:
		c.trim(m)
	}
	return c.w
}

// set moves W to n. A measurement from the W says nothing about n.
func (c *Controller) set(n int) {
	if n < c.min {
		n = c.min
	}
	if n > c.max {
		n = c.max
	}
	c.prevW = c.w
	c.w = n
	c.dwell = c.DwellN
	c.resetWindow()
}

func (c *Controller) resetWindow() {
	c.sum, c.n = 0, 0
	c.hasLast = false
}

func (c *Controller) toHold() {
	c.ph = holding
	c.hold, c.holdSum, c.holdN = 0, 0, 0
}

// climb compares the settled throughput at this W with refT.
func (c *Controller) climb(m float64, s Sample) {
	// Throughput that still rises at a constant W is a warm-up. It is not a gain from W.
	if !c.hasLast || m > c.last*(1+c.Margin) {
		c.last, c.hasLast = m, true
		return
	}
	cur := (m + c.last) / 2
	c.hasLast = false

	if c.hasRef && cur <= c.refT*(1+c.Margin) {
		// The last increase didn't help: revert to the knee and hold.
		c.set(c.prevW)
		c.toHold()
		return
	}
	// The last increase paid off; keep climbing if allowed.
	c.refT, c.hasRef = cur, true
	if c.canGrow(s) && c.w < c.max {
		c.set(c.grow())
		return
	}
	c.toHold()
}

// held measures the plateau, then probes up and down in turn.
func (c *Controller) held(m float64, s Sample) {
	c.holdSum += m
	c.holdN++
	c.hold += c.WindowN
	if c.hold < c.Reprobe {
		return
	}
	base := c.holdSum / float64(c.holdN)
	c.refT, c.hasRef = base, true
	c.toHold()

	c.probeUp = !c.probeUp
	up := c.canGrow(s) && c.w < c.max
	switch {
	case c.probeUp && up:
		c.set(c.grow())
		c.ph = climbing
	case c.w > c.min && base > 0:
		// Trim toward the minimum W that still sustains the plateau.
		c.trimRef, c.trimStep = base, 1
		c.set(c.w - 1)
		c.ph = trimming
	case up:
		c.set(c.grow())
		c.ph = climbing
	}
}

// trim cuts deeper while a cut is free. The step doubles, so a W far above the plateau comes down fast.
func (c *Controller) trim(m float64) {
	if m < c.trimRef*(1-c.Margin) {
		// It cost throughput: restore and hold.
		c.set(c.prevW)
		c.toHold()
		return
	}
	if c.w <= c.min {
		c.toHold()
		return
	}
	c.trimStep *= 2
	if most := c.w / 3; c.trimStep > most {
		c.trimStep = most
	}
	if c.trimStep < 1 {
		c.trimStep = 1
	}
	c.set(c.w - c.trimStep)
}
