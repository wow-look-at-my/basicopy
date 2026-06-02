//go:build !linux

package fsx

import "os"

// copyData on non-Linux platforms uses the portable buffered copy. Native fast
// paths (clonefile on macOS, CopyFileEx / block-clone on Windows) are layered on
// here later; the buffered path is always correct.
func copyData(dst, src *os.File, info os.FileInfo, bufSize int) (int64, error) {
	return plainCopy(dst, src, bufSize)
}
