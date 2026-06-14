package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryBackoffCapsAtTwoSeconds(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 100 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	assert.Equal(t, 100*time.Millisecond, retryBackoff(0))
	assert.Equal(t, 200*time.Millisecond, retryBackoff(1))
	assert.Equal(t, 2*time.Second, retryBackoff(8))
}
