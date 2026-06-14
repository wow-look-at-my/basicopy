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

// isNoSpace reports whether err is ENOSPC: the destination filesystem is full.
// This is never transient -- the disk will not drain itself mid-run -- and every
// remaining file would fail identically, so the caller aborts the whole run
// rather than retrying or grinding through the rest of the tree.
func isNoSpace(err error) bool {
	return errors.Is(err, unix.ENOSPC)
}
