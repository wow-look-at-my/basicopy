//go:build !unix

package engine

import "os"

// fileKey reports no inode identity off Unix, so hardlink preservation is a no-op
// there (files are simply copied).
func fileKey(info os.FileInfo) (key string, nlink uint64, ok bool) {
	return "", 0, false
}

func fileDev(info os.FileInfo) (uint64, bool) { return 0, false }
