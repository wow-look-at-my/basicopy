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

func TestIsNoSpace(t *testing.T) {
	assert.True(t, isNoSpace(fmt.Errorf("write big.bin: %w", unix.ENOSPC)), "wrapped ENOSPC is a full disk")
	assert.False(t, isNoSpace(unix.EAGAIN), "a transient error is not disk-full")
	assert.False(t, isNoSpace(nil))
}
