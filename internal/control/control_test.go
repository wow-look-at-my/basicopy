package control

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// curve models achievable throughput as a function of worker count: a saturating
// hyperbola peak*w/(w+knee), optionally capped by a hard ceiling (a bus/link
// limit that more workers cannot exceed).
type curve struct {
	peak, knee, ceiling float64
}

func (c curve) at(w int) float64 {
	t := c.peak * float64(w) / (float64(w) + c.knee)
	if c.ceiling > 0 && t > c.ceiling {
		t = c.ceiling
	}
	return t
}

// run drives the controller for n ticks against cv, applying guard to each
// sample, and returns the W chosen each tick.
func run(c *Controller, cv curve, guard func(*Sample), n int) []int {
	ws := make([]int, 0, n)
	w := c.W()
	for i := 0; i < n; i++ {
		noise := 1 + 0.02*math.Sin(float64(i)*1.3) // deterministic +-2%
		s := Sample{Throughput: cv.at(w) * noise, SysCPU: -1, DevUtil: -1}
		if guard != nil {
			guard(&s)
		}
		w = c.Update(s)
		ws = append(ws, w)
	}
	return ws
}

func tailAvg(ws []int, n int) float64 {
	if n > len(ws) {
		n = len(ws)
	}
	sum := 0
	for _, w := range ws[len(ws)-n:] {
		sum += w
	}
	return float64(sum) / float64(n)
}

func maxOf(ws []int) int {
	m := 0
	for _, w := range ws {
		if w > m {
			m = w
		}
	}
	return m
}

func TestClimbsAndStaysBounded(t *testing.T) {
	c := New(1, 64, 2)
	ws := run(c, curve{peak: 1e9, knee: 8}, nil, 400)
	assert.LessOrEqual(t, maxOf(ws), 64, "must never exceed max")
	avg := tailAvg(ws, 60)
	assert.Greater(t, avg, 8.0, "should climb well above the start")
	assert.Less(t, avg, 64.0, "a gentle plateau should not peg at max")
}

func TestBusCeilingTrimsToMinW(t *testing.T) {
	// Ceiling reached around w=3; extra workers buy nothing.
	c := New(1, 64, 2)
	ws := run(c, curve{peak: 1e9, knee: 4, ceiling: 0.4e9}, nil, 500)
	avg := tailAvg(ws, 80)
	assert.LessOrEqual(t, avg, 12.0, "under a bus ceiling it must trim to a small W, not run to max")
	assert.GreaterOrEqual(t, avg, 2.0, "but enough to actually saturate the ceiling")
}

func TestMaxBoundReached(t *testing.T) {
	// A knee so large the curve keeps improving -> climb to the ceiling.
	c := New(1, 32, 2)
	ws := run(c, curve{peak: 1e9, knee: 1e6}, nil, 300)
	assert.LessOrEqual(t, maxOf(ws), 32, "never exceed max")
	assert.GreaterOrEqual(t, tailAvg(ws, 40), 28.0, "should drive W up to near the ceiling")
}

func TestCPUGuardStaysLow(t *testing.T) {
	c := New(1, 64, 4)
	pinned := func(s *Sample) { s.SysCPU = 0.99 } // system CPU pinned -> never grow
	ws := run(c, curve{peak: 1e9, knee: 8}, pinned, 300)
	assert.LessOrEqual(t, tailAvg(ws, 40), 5.0, "CPU-bound: hold/trim, don't pile on workers")
}

func TestHDDUtilGuardStaysLow(t *testing.T) {
	c := New(1, 64, 4)
	saturated := func(s *Sample) { s.Rotational = true; s.DevUtil = 99 }
	ws := run(c, curve{peak: 1e9, knee: 8}, saturated, 300)
	assert.LessOrEqual(t, tailAvg(ws, 40), 5.0, "saturated HDD: don't grow into seek thrash")
}

func TestLowKneeSettlesLowerThanHighKnee(t *testing.T) {
	lo := New(1, 128, 2)
	hi := New(1, 128, 2)
	wlo := tailAvg(run(lo, curve{peak: 1e9, knee: 4}, nil, 500), 80)
	whi := tailAvg(run(hi, curve{peak: 1e9, knee: 64}, nil, 500), 80)
	assert.Less(t, wlo, whi, "a workload that saturates sooner should settle at a lower W")
}
