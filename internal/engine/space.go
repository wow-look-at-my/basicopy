// availBytes reports free space; a package var lets tests simulate a full destination.

package engine

import (
	"fmt"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// availBytes reports free space; a package var lets tests simulate a full destination.
var availBytes = func(fsys vfs.FS, dir string) (int64, bool) { return fsys.Avail(dir) }

func (r *runner) initSpaceGuard(dir string) {
	if r.opts.DryRun || r.spaceCheck {
		return // dry runs don't write; and seed a single time across multiple
	}
	if avail, ok := availBytes(r.dfs, dir); ok {
		r.freeBytes.Store(avail)
		r.spaceCheck = true
	}
}

// reserveSpace checks that the destination still has room for a file of need bytes before it is written, aborting the run cleanly if not. A running estimate (seeded by initSpaceGuard, decremented per file) avoids a statfs on every file. So the guard is correct on a compressing/cloning filesystem like ZFS where the estimate intentionally drifts low (it never aborts without a fresh, real reading). This holds when the
// estimate is exhausted a real statfs confirms the figure before any abort.
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
