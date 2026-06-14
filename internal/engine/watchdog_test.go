package engine

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

func TestStallDetector(t *testing.T) {
	d := &stallDetector{timeout: 30 * time.Second}
	t0 := time.Now()
	assert.False(t, d.update(0, t0), "first sample primes the baseline")
	assert.False(t, d.update(0, t0.Add(10*time.Second)), "10s without progress is under the timeout")
	assert.True(t, d.update(0, t0.Add(31*time.Second)), "31s without progress is a stall")

	// Forward progress resets the window.
	assert.False(t, d.update(5, t0.Add(32*time.Second)))
	assert.False(t, d.update(5, t0.Add(50*time.Second)))
	assert.True(t, d.update(5, t0.Add(63*time.Second)), "stalls again 30s after the last progress")
}

func TestOnStallNonTTYAborts(t *testing.T) {
	r := &runner{opts: &options.Options{}, stderr: io.Discard}
	cont, _ := r.onStall()
	assert.False(t, cont, "without a controlling terminal, a stall aborts")
}

// TestProgressCountTracksInflightBytes guards the stall-watchdog fix: bytes
// written while a large file is still being copied (moved) must count as forward
// progress, even though no file has completed yet (bytes/files unchanged). Without
// this, a single multi-GB file in flight looks like a stall and aborts the run.
func TestProgressCountTracksInflightBytes(t *testing.T) {
	r := &runner{opts: &options.Options{}}
	base := r.progressCount()
	r.moved.Add(64 << 20) // 64 MiB streamed mid-copy, before the file finishes
	assert.EqualValues(t, base+(64<<20), r.progressCount(),
		"in-flight bytes must register as forward progress")
}

func TestProgressCountTracksDiscoveredWork(t *testing.T) {
	r := &runner{opts: &options.Options{}}
	base := r.progressCount()
	r.totalFiles.Add(3)
	r.totalBytes.Add(1024)
	assert.Greater(t, r.progressCount(), base,
		"lookahead discovery must register as forward progress")
}
