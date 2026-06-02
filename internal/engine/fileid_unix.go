//go:build unix

package engine

import (
	"fmt"
	"os"
	"syscall"
)

// fileKey returns a stable per-inode key and the file's link count. ok is false
// when the platform doesn't expose inode info. A link count > 1 means the file is
// hardlinked and its link topology should be preserved.
func fileKey(info os.FileInfo) (key string, nlink uint64, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, false
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), uint64(st.Nlink), true
}

// fileDev returns the device number backing info, for mount-point detection.
func fileDev(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
