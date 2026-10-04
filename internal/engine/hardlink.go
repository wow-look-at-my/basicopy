package engine

import (
	"fmt"
	"os"

	"github.com/wow-look-at-my/basicopy/internal/scan"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// hlPrimary records a multiply-linked inode's earliest destination; adopted
// means an unchanged file was kept as-is, not recopied.
type hlPrimary struct {
	dst     string
	adopted bool
}

type linkEnt struct {
	target, dst string
	parent      *dirState
	srcFS       vfs.FS
	src         string
}

// handleRegular preserves hardlinks (default-on): the earliest path to a
// multiply- linked inode is copied; subsequent paths are recorded to be
// link()ed to that earliest copy after all copies finish. Single-link files
// are copied. Dry runs use the same hardlink bookkeeping so their itemization
// matches what a real run would do (a single copy plus links, not N
// independent copies).
func (r *runner) handleRegular(root *srcRoot, srcPath, dstPath string, fi os.FileInfo, parent *dirState) {
	if !r.opts.NoHardlinks {
		key, nlink, ok := root.fs.FileID(fi)
		if !ok {
			r.noteNoFileIDs(root.fs)
		}
		if ok && nlink > 1 {
			r.handleMultiLink(root, srcPath, dstPath, fi, parent, key)
			return
		}
	}
	if r.opts.DryRun {
		// A dry run has no worker pool; decide skip-vs-copy right here.
		v := scan.Compare(root.fs, srcPath, fi, r.dfs, dstPath, r.opts.Checksum)
		if !v.NeedCopy {
			r.finishUnchangedFile(root.fs, srcPath, dstPath, fi, v)
			return
		}
		r.enqueueFile(root, srcPath, dstPath, fi, parent, false, v.Reason)
		return
	}
	// Runs on the pool, not the walk goroutine: --checksum needs full reads.
	r.enqueueFile(root, srcPath, dstPath, fi, parent, true, "")
}

// noteNoFileIDs prints a single time, under --verbose: SFTP has no inode or
// link count, so a remote source's hardlinks copy as independent files.
func (r *runner) noteNoFileIDs(fsys vfs.FS) {
	if r.noFileIDsNoted || fsys.Local() {
		return
	}
	r.noFileIDsNoted = true
	r.verbose("%s: SSH exposes no inode identity; hardlinks in the source cannot be detected", fsys.Host())
}

// handleMultiLink handles a single path to a multiply-linked source inode.
// The earliest path becomes the primary: it is copied — or, when its
// destination is already up to date, the existing destination file is adopted
// as-is. Every later path to the same inode is hardlinked to the primary's
// destination after all copies finish, unless it already IS the adopted
// primary's inode (nothing to do).
//
// Adopting an unchanged primary (rather than skipping it before the hardlink
// bookkeeping, as.
func (r *runner) handleMultiLink(root *srcRoot, srcPath, dstPath string, fi os.FileInfo, parent *dirState, key string) {
	if p, seen := r.hardlinkMap[key]; seen {
		if p.adopted && r.sameDestFile(p.dst, dstPath) {
			r.skipped.Add(1)
			r.verbose("skip unchanged %s", r.dfs.Display(dstPath))
			r.moveUnchanged(root.fs, srcPath, dstPath)
			return
		}
		r.addDirWork(parent)
		r.hardlinks = append(r.hardlinks, linkEnt{target: p.dst, dst: dstPath, parent: parent, srcFS: root.fs, src: srcPath})
		return
	}
	v := scan.Compare(root.fs, srcPath, fi, r.dfs, dstPath, r.opts.Checksum)
	if !v.NeedCopy {
		r.hardlinkMap[key] = hlPrimary{dst: dstPath, adopted: true}
		r.finishUnchangedFile(root.fs, srcPath, dstPath, fi, v)
		return
	}
	r.hardlinkMap[key] = hlPrimary{dst: dstPath}
	r.enqueueFile(root, srcPath, dstPath, fi, parent, false, v.Reason)
}

// sameDestFile reports whether a and b are the same file. An SFTP destination
// (no inode identity) reports false, so the link is recreated, not assumed.
func (r *runner) sameDestFile(a, b string) bool {
	fa, err := r.dfs.Lstat(a)
	if err != nil {
		return false
	}
	fb, err := r.dfs.Lstat(b)
	if err != nil {
		return false
	}
	return r.dfs.SameFile(fa, fb)
}

// applyHardlinks creates the recorded secondary hardlinks after all primary
// copies have completed. A dry run itemizes and counts them without linking.
func (r *runner) applyHardlinks() {
	for _, l := range r.hardlinks {
		if r.opts.DryRun {
			r.linked.Add(1)
			r.item("would hardlink %s => %s", r.dfs.Display(l.dst), r.dfs.Display(l.target))
			continue
		}
		_ = r.dfs.Remove(l.dst) // tolerate a leftover from a prior run
		if err := r.dfs.Link(l.target, l.dst); err != nil {
			r.fail(fmt.Errorf("hardlink %s -> %s: %w", r.dfs.Display(l.dst), r.dfs.Display(l.target), err))
			r.completeDirWork(l.parent)
			continue
		}
		r.linked.Add(1)
		r.item("%s => %s (hardlink)", r.dfs.Display(l.dst), r.dfs.Display(l.target))
		r.moveSource(l.srcFS, l.src, l.dst)
		r.completeDirWork(l.parent)
	}
}
