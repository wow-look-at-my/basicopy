//go:build !unix

package scan

import "io/fs"

// FileOwner reports no ownership info off Unix, so owner drift is never
// detected there.
func FileOwner(info fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
