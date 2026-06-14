package device

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassString(t *testing.T) {
	assert.Equal(t, "unknown", Unknown.String())
	assert.Equal(t, "SSD", SSD.String())
	assert.Equal(t, "HDD", HDD.String())
	assert.Equal(t, "unknown", Class(99).String())
}

func TestInfoRotational(t *testing.T) {
	assert.False(t, Info{Class: SSD}.Rotational())
	assert.True(t, Info{Class: HDD}.Rotational())
	assert.False(t, Info{Class: Unknown}.Rotational())
}

func TestUtilSamplerWithoutDevice(t *testing.T) {
	util, ok := NewUtilSampler("").Sample()
	assert.Zero(t, util)
	assert.False(t, ok)
}

func TestUtilSamplerUnsupportedDevice(t *testing.T) {
	old := readIOTicksForSample
	readIOTicksForSample = func(name string) (uint64, bool) { return 0, false }
	defer func() { readIOTicksForSample = old }()

	util, ok := NewUtilSampler("disk0").Sample()
	assert.Zero(t, util)
	assert.False(t, ok)
}

func TestUtilSamplerSamplesAndClamps(t *testing.T) {
	old := readIOTicksForSample
	ticks := []uint64{10, 15, 1_000_000}
	readIOTicksForSample = func(name string) (uint64, bool) {
		next := ticks[0]
		ticks = ticks[1:]
		return next, true
	}
	defer func() { readIOTicksForSample = old }()

	s := NewUtilSampler("disk0")
	util, ok := s.Sample()
	assert.Zero(t, util)
	assert.False(t, ok)

	s.prevTime = time.Now().Add(-100 * time.Millisecond)
	util, ok = s.Sample()
	assert.True(t, ok)
	assert.Greater(t, util, 0.0)
	assert.LessOrEqual(t, util, 100.0)

	s.prevTime = time.Now().Add(-time.Millisecond)
	util, ok = s.Sample()
	assert.True(t, ok)
	assert.Equal(t, 100.0, util)
}
