//go:build unix

package fsx

import (
	"os"
	"syscall"
)

// preserveOwner sets dst's uid/gid to match info on Unix systems. It is
// best-effort: lacking privilege (EPERM) is the common case for unprivileged
// copies and is not treated as fatal.
func preserveOwner(dst string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Lchown(dst, int(st.Uid), int(st.Gid)); err != nil {
		if os.IsPermission(err) {
			return nil
		}
		return err
	}
	return nil
}
