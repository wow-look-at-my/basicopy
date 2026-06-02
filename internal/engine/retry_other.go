//go:build !unix

package engine

// retryable conservatively reports nothing as retryable off Unix; copies still
// succeed, just without transient-error retries.
func retryable(err error) bool { return false }
