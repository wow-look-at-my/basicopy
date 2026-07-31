package engine

import (
	"fmt"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// availBytes reports the space available on the filesystem backing a path. It is
// a package var (not a direct call) so tests can simulate a full destination
// without needing a real size-limited filesystem. A remote destination answers
// through SFTP's statvfs extension; a server without it reports "unknown" and
// the guard stays off.
var availBytes = func(fsys vfs.FS, dir string) (int64, bool) { return fsys.Avail(dir) }

// initSpaceGuard records the destination's currently-available space so copyOne
// can refuse a file that would not fit, stopping the run *before* an ENOSPC write
// instead of after one. It is a no-op (guard disabled) for a dry run or when the
// figure cannot be read (e.g. off Unix, or a filesystem statfs rejects).
func (r *runner) initSpaceGuard(dir string) {
	if r.opts.DryRun || r.spaceCheck {
		return // dry runs don't write; and seed only once across multiple sources
	}
	if avail, ok := availBytes(r.dfs, dir); ok {
		r.freeBytes.Store(avail)
		r.spaceCheck = true
	}
}

// reserveSpace checks that the destination still has room for a file of need
// bytes before it is written, aborting the run cleanly if not. A running estimate
// (seeded by initSpaceGuard, decremented per file) avoids a statfs on every file;
// when the estimate is exhausted a real statfs confirms the figure before any
// abort, so the guard is correct on a compressing/cloning filesystem like ZFS
// where the estimate intentionally drifts low (it never aborts without a fresh,
// real reading). Returns false (after aborting) when the file cannot fit.
func (r *runner) reserveSpace(dstPath string, need int64) bool {
	if !r.spaceCheck {
		return true
	}
	if r.freeBytes.Add(-need) >= 0 {
		return true
	}
	// The estimate says we may be out of room; get the real figure before deciding.
	avail, ok := availBytes(r.dfs, r.dfs.Dir(dstPath))
	if !ok {
		return true // can't measure now; the ENOSPC abort is the backstop
	}
	r.freeBytes.Store(avail - need)
	if avail < need {
		r.elog("basicopy: destination is out of space: %s needs %s but only %s is free",
			r.dfs.Display(dstPath), fmtBytes(need), fmtBytes(avail))
		r.setAbort(fmt.Errorf("destination is out of space"))
		return false
	}
	return true
}
