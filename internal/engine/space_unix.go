//go:build unix

package engine

import "golang.org/x/sys/unix"

// statfsAvail returns the number of bytes available to an unprivileged caller on
// the filesystem backing path, and whether the figure could be read. It uses the
// caller-available block count (Bavail), not the total free count, so it matches
// what a copy can actually write.
func statfsAvail(path string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
