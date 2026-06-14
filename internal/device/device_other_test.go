//go:build !linux

package device

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnsupportedBackend(t *testing.T) {
	assert.Equal(t, Info{}, Lookup("/tmp"))

	ticks, ok := readIOTicks("disk0")
	assert.Zero(t, ticks)
	assert.False(t, ok)
}
