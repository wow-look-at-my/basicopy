package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestControllerStartW(t *testing.T) {
	assert.Equal(t, 2, controllerStartW(true, 8), "rotational media seeds conservatively at 2")
	assert.Equal(t, 1, controllerStartW(true, 1), "rotational respects a max below the seed")
	n := controllerStartW(false, 64)
	assert.GreaterOrEqual(t, n, 4, "non-rotational seeds with enough queue depth")
	assert.LessOrEqual(t, n, 64)
	assert.Equal(t, 3, controllerStartW(false, 3), "the seed is clamped down to a small max")
}

func TestSampleUtilNoSamplers(t *testing.T) {
	assert.Equal(t, -1.0, sampleUtil(nil, nil), "with no samplers, device util is unknown (-1)")
}
