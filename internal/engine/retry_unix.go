//go:build unix

package engine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// retryable reports whether err is a transient I/O error worth retrying after a
// short backoff (resource contention, interrupted calls, or network-filesystem
// blips on a mounted share).
func retryable(err error) bool {
	if err == nil {
		return false
	}
	for _, e := range []error{unix.EAGAIN, unix.EBUSY, unix.EINTR, unix.ETIMEDOUT, unix.ESTALE, unix.ECONNRESET, unix.ENOMEM} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
