package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestControllerStartW(t *testing.T) {
	assert.Equal(t, 2, controllerStartW(true, false, 8), "rotational media seeds conservatively at 2")
	assert.Equal(t, 1, controllerStartW(true, false, 1), "rotational respects a max below the seed")
	n := controllerStartW(false, false, 64)
	assert.GreaterOrEqual(t, n, 4, "non-rotational seeds with enough queue depth")
	assert.LessOrEqual(t, n, 64)
	assert.Equal(t, 3, controllerStartW(false, false, 3), "the seed is clamped down to a small max")
	assert.Equal(t, 4, controllerStartW(false, true, 256), "a remote copy starts small, whatever the cores of this machine")
	assert.Equal(t, 2, controllerStartW(true, true, 256), "a rotational disk still wins over a remote end")
	assert.Equal(t, 3, controllerStartW(false, true, 3), "the remote seed respects a small max")
}

func TestSampleUtilNoSamplers(t *testing.T) {
	assert.Equal(t, -1.0, sampleUtil(nil, nil), "with no samplers, device util is unknown (-1)")
}
