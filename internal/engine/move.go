// --move: a rename when the source and destination share a filesystem, else a
// copy followed by removal of each source entry that reached the destination.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// movedEntry is a source entry that is safe to remove: dst holds its content.
type movedEntry struct {
	srcFS    vfs.FS
	src, dst string
}

// transferVerb is the itemize verb for a file transfer.
func (r *runner) transferVerb() string {
	if r.opts.Move {
		return "move"
	}
	return "copy"
}

// renameRoot moves a whole source root with one rename. It returns false when
// the caller must copy instead. A failed rename (EXDEV, a non-empty target
// directory) is not an error: the copy path then does the move.
func (r *runner) renameRoot(sfs vfs.FS, src, dst string, fi os.FileInfo) bool {
	o := r.opts
	if !o.Move || o.DryRun || !vfs.SameHost(sfs, r.dfs) {
		return false
	}
	// A rename cannot skip an excluded entry, stop at a mount point, or split a hardlink.
	if len(o.Exclude) > 0 || len(o.Include) > 0 || o.OneFileSystem || o.NoHardlinks {
		return false
	}
	di, err := r.dfs.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false
	case fi.IsDir() || di.IsDir():
		// A rename replaces only an empty directory; the copy path merges into a full one.
		return false
	case r.dfs.SameFile(fi, di):
		return false
	}
	if err := r.dfs.Rename(src, dst); err != nil {
		r.verbose("rename %s: %v; copying instead", sfs.Display(src), err)
		return false
	}
	r.renamed.Add(1)
	r.item("rename %s -> %s", sfs.Display(src), r.dfs.Display(dst))
	return true
}

// moveSource records a source entry for removal after the run. The removal
// waits for --mirror, which reads the source to find extraneous entries.
func (r *runner) moveSource(srcFS vfs.FS, src, dst string) {
	if !r.opts.Move || r.opts.DryRun {
		return
	}
	r.moveMu.Lock()
	r.movedFiles = append(r.movedFiles, movedEntry{srcFS: srcFS, src: src, dst: dst})
	r.moveMu.Unlock()
}

// moveUnchanged removes the source of a file that the destination already holds.
func (r *runner) moveUnchanged(srcFS vfs.FS, src, dst string) {
	if !r.opts.Move {
		return
	}
	r.item("%sremove source %s (unchanged at %s)", r.would(), srcFS.Display(src), r.dfs.Display(dst))
	r.moveSource(srcFS, src, dst)
}

// moveDir records a walked source directory. Walk order is post-order, so a
// child is always removed before its parent.
func (r *runner) moveDir(root *srcRoot, src, dst string) {
	if !r.opts.Move || r.opts.DryRun || src == root.keep {
		return
	}
	r.movedDirs = append(r.movedDirs, movedEntry{srcFS: root.fs, src: src, dst: dst})
}

// removeMovedSources deletes every recorded source entry: files in parallel at
// the measured worker limit, then directories deepest first.
func (r *runner) removeMovedSources(ctx context.Context) {
	jobs := make(chan movedEntry)
	var wg sync.WaitGroup
	for i := 0; i < r.gate.current(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				r.removeSource(e, false)
			}
		}()
	}
	for _, e := range r.movedFiles {
		if ctx.Err() != nil {
			break
		}
		jobs <- e
	}
	close(jobs)
	wg.Wait()
	for _, e := range r.movedDirs {
		if ctx.Err() != nil {
			return
		}
		r.removeSource(e, true)
	}
}

// removeSource deletes one source entry. A directory that still has entries
// (excluded, failed, or a skipped special file) stays without an error.
func (r *runner) removeSource(e movedEntry, dir bool) {
	if r.sameEntry(e) {
		r.fail(fmt.Errorf("not removing %s: it is the destination %s", e.srcFS.Display(e.src), r.dfs.Display(e.dst)))
		return
	}
	err := e.srcFS.Remove(e.src)
	if err == nil {
		r.removed.Add(1)
		return
	}
	if dir {
		if left, rdErr := e.srcFS.ReadDir(e.src); rdErr == nil && len(left) > 0 {
			r.verbose("keep source directory %s (%d entries left)", e.srcFS.Display(e.src), len(left))
			return
		}
	}
	r.fail(fmt.Errorf("remove source %s: %w", e.srcFS.Display(e.src), err))
}

// sameEntry catches a destination that aliases its source through a symlinked
// directory. Removing the source then deletes the only copy. Only a local pair
// has the inode identity to tell.
func (r *runner) sameEntry(e movedEntry) bool {
	if !e.srcFS.Local() || !r.dfs.Local() {
		return false
	}
	si, err := e.srcFS.Lstat(e.src)
	if err != nil {
		return false
	}
	di, err := r.dfs.Lstat(e.dst)
	if err != nil {
		return false
	}
	return r.dfs.SameFile(si, di)
}
