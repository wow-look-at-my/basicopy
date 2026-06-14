//go:build !unix

package engine

// retryable conservatively reports nothing as retryable off Unix; copies still
// succeed, just without transient-error retries.
func retryable(err error) bool { return false }

// isNoSpace conservatively reports false off Unix (no portable ENOSPC check is
// wired up here); a full destination is still surfaced as per-file failures.
func isNoSpace(err error) bool { return false }
