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
