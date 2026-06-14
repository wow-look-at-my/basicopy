//go:build unix

package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"
)

func TestRetryable(t *testing.T) {
	assert.True(t, retryable(fmt.Errorf("copy x: %w", unix.EAGAIN)), "wrapped EAGAIN is transient")
	assert.True(t, retryable(unix.ESTALE), "stale NFS handle is transient")
	assert.False(t, retryable(errors.New("permanent failure")))
	assert.False(t, retryable(nil))
}
