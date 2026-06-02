//go:build linux

package sysload

import (
	"testing"
	"time"

	"github.com/wow-look-at-my/testify/assert"
	"github.com/wow-look-at-my/testify/require"
)

func TestParseCPULine(t *testing.T) {
	stat := "cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 25 0 12 200 12 0 0 0 0 0\nintr 1 2 3\n"
	idle, total, ok := parseCPULine(stat)
	require.True(t, ok)
	assert.EqualValues(t, 850, idle)   // idle(800) + iowait(50)
	assert.EqualValues(t, 1000, total) // sum of all fields
}

func TestParseCPULineBad(t *testing.T) {
	_, _, ok := parseCPULine("intr 1 2 3\nno cpu line here\n")
	assert.False(t, ok)
	_, _, ok = parseCPULine("cpu  abc def\n")
	assert.False(t, ok)
}

func TestSamplerOnRealSystem(t *testing.T) {
	s := New()
	_, ok := s.Sample()
	assert.False(t, ok, "first sample primes the baseline")

	// Let enough wall time pass that the jiffy counters advance (total > 0),
	// while burning a little CPU so there is measurable busy time.
	deadline := time.Now().Add(60 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	_ = x

	busy, ok := s.Sample()
	require.True(t, ok, "second sample should read /proc/stat")
	assert.GreaterOrEqual(t, busy, 0.0)
	assert.LessOrEqual(t, busy, 1.0)
}
