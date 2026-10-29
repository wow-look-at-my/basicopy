//go:build !linux

// copyData falls back to the portable buffered copy; no OS-native fast path yet.

package fsx

import "os"

// copyData falls back to the portable buffered copy; no OS-native fast path yet.
func copyData(dst, src *os.File, info os.FileInfo, o CopyOptions, _ *fileDedup) (int64, error) {
	return plainCopy(dst, src, o)
}
