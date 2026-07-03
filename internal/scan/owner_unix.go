//go:build unix

package scan

import (
	"io/fs"
	"syscall"
)

// FileOwner extracts the Unix uid/gid backing a FileInfo. ok is false when the
// platform (or the FileInfo's origin) doesn't expose ownership.
func FileOwner(info fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
