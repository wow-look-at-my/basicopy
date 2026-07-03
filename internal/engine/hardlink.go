package engine

import (
	"fmt"
	"os"

	"github.com/wow-look-at-my/basicopy/internal/scan"
)

// hlPrimary records where a multiply-linked inode's first destination lives.
// adopted means an existing, unchanged destination file was kept as the link
// target rather than recopied this run.
type hlPrimary struct {
	dst     string
	adopted bool
}

type linkEnt struct {
	target, dst string
	parent      *dirState
}

// handleRegular preserves hardlinks (default-on): the first path to a multiply-
// linked inode is copied; subsequent paths are recorded to be link()ed to that
// first copy after all copies finish. Single-link files are just copied. Dry
// runs use the same hardlink bookkeeping so their itemization matches what a
// real run would do (one copy plus links, not N independent copies).
func (r *runner) handleRegular(srcPath, dstPath string, fi os.FileInfo, parent *dirState) {
	if !r.opts.NoHardlinks {
		if key, nlink, ok := fileKey(fi); ok && nlink > 1 {
			r.handleMultiLink(srcPath, dstPath, fi, parent, key)
			return
		}
	}
	if r.opts.DryRun {
		// A dry run has no worker pool; decide skip-vs-copy right here.
		v := scan.Compare(srcPath, fi, dstPath, r.opts.Checksum)
		if !v.NeedCopy {
			r.finishUnchangedFile(dstPath, fi, v)
			return
		}
		r.enqueueFile(srcPath, dstPath, fi, parent, false, v.Reason)
		return
	}
	// The skip-unchanged decision runs on the worker pool (checkUnchanged), not
	// here: with --checksum it means two full content reads, and doing that on
	// the single walk goroutine would serialize all hashing while the pool sat
	// idle. Multi-link files (above) still decide on the walk goroutine because
	// the answer feeds the walk-owned hardlink bookkeeping.
	r.enqueueFile(srcPath, dstPath, fi, parent, true, "")
}

// handleMultiLink handles one path to a multiply-linked source inode. The first
// path becomes the primary: it is copied — or, when its destination is already
// up to date, the existing destination file is adopted as-is. Every later path
// to the same inode is hardlinked to the primary's destination after all copies
// finish, unless it already IS the adopted primary's inode (nothing to do).
//
// Adopting an unchanged primary (rather than skipping it before the hardlink
// bookkeeping, as this used to) matters for incremental runs: without a
// recorded primary, a secondary missing from the destination was recopied as an
// independent duplicate, silently losing the hardlink structure and doubling
// the stored data.
func (r *runner) handleMultiLink(srcPath, dstPath string, fi os.FileInfo, parent *dirState, key string) {
	if p, seen := r.hardlinkMap[key]; seen {
		if p.adopted && sameFile(p.dst, dstPath) {
			r.skipped.Add(1)
			r.verbose("skip unchanged %s", dstPath)
			return
		}
		r.addDirWork(parent)
		r.hardlinks = append(r.hardlinks, linkEnt{target: p.dst, dst: dstPath, parent: parent})
		return
	}
	v := scan.Compare(srcPath, fi, dstPath, r.opts.Checksum)
	if !v.NeedCopy {
		r.hardlinkMap[key] = hlPrimary{dst: dstPath, adopted: true}
		r.finishUnchangedFile(dstPath, fi, v)
		return
	}
	r.hardlinkMap[key] = hlPrimary{dst: dstPath}
	r.enqueueFile(srcPath, dstPath, fi, parent, false, v.Reason)
}

// sameFile reports whether two paths currently refer to the same inode.
func sameFile(a, b string) bool {
	fa, err := os.Lstat(a)
	if err != nil {
		return false
	}
	fb, err := os.Lstat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// applyHardlinks creates the recorded secondary hardlinks after all primary
// copies have completed. A dry run itemizes and counts them without linking.
func (r *runner) applyHardlinks() {
	for _, l := range r.hardlinks {
		if r.opts.DryRun {
			r.linked.Add(1)
			r.item("would hardlink %s => %s", l.dst, l.target)
			continue
		}
		_ = os.Remove(l.dst) // tolerate a leftover from a prior run
		if err := os.Link(l.target, l.dst); err != nil {
			r.fail(fmt.Errorf("hardlink %s -> %s: %w", l.dst, l.target, err))
			r.completeDirWork(l.parent)
			continue
		}
		r.linked.Add(1)
		r.item("%s => %s (hardlink)", l.dst, l.target)
		r.completeDirWork(l.parent)
	}
}
