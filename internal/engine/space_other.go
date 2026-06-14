//go:build !unix

package engine

// statfsAvail is unavailable off Unix, so the pre-write space guard is disabled
// there (the ENOSPC abort still stops a full destination, just reactively).
func statfsAvail(path string) (int64, bool) { return 0, false }
