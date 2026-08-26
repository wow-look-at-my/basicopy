//go:build !unix

package engine

// retryable reports nothing as retryable off Unix; copies still succeed without retries.
func retryable(err error) bool { return false }

// isNoSpace always reports false off Unix; a full destination surfaces as per-file failures.
func isNoSpace(err error) bool { return false }
