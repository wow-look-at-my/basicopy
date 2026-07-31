//go:build unix

package vfs

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// statfsAvail returns the bytes available to an unprivileged caller on the
// filesystem backing path (Bavail, not the total free count, so it matches what
// a copy can actually write).
func statfsAvail(path string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

func localOwner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

func localFileID(fi os.FileInfo) (key string, nlink uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, false
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), uint64(st.Nlink), true
}

func localDev(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
