package sysload

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSamplerStartsUnprimed(t *testing.T) {
	s := New()
	assert.False(t, s.primed)
	assert.Zero(t, s.prevIdle)
	assert.Zero(t, s.prevTotal)
}

func TestSamplerUnsupportedPlatform(t *testing.T) {
	old := readCPUTimesForSample
	readCPUTimesForSample = func() (idle, total uint64, ok bool) { return 0, 0, false }
	defer func() { readCPUTimesForSample = old }()

	busy, ok := New().Sample()
	assert.Zero(t, busy)
	assert.False(t, ok)
}

func TestSamplerSamplesAndClamps(t *testing.T) {
	old := readCPUTimesForSample
	samples := []struct {
		idle, total uint64
	}{
		{idle: 100, total: 200},
		{idle: 125, total: 300},
		{idle: 125, total: 300},
		{idle: 400, total: 500},
	}
	readCPUTimesForSample = func() (idle, total uint64, ok bool) {
		next := samples[0]
		samples = samples[1:]
		return next.idle, next.total, true
	}
	defer func() { readCPUTimesForSample = old }()

	s := New()
	busy, ok := s.Sample()
	assert.Zero(t, busy)
	assert.False(t, ok)

	busy, ok = s.Sample()
	assert.True(t, ok)
	assert.Equal(t, 0.75, busy)

	busy, ok = s.Sample()
	assert.Zero(t, busy)
	assert.False(t, ok)

	busy, ok = s.Sample()
	assert.True(t, ok)
	assert.Equal(t, 0.0, busy)
}
