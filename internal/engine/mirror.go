// The --mirror pass: deleting destination entries that have no counterpart in
// the source (robocopy's /MIR). It runs after copying, so the destination
// already reflects the source's content.
package engine

import (
	"fmt"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// mirrorExtraneous deletes destination entries that have no counterpart in the
// source (the --mirror / robocopy /MIR behavior). It runs after copying so the
// destination already reflects the source's content, and only under the roots
// the walk actually processed.
func (r *runner) mirrorExtraneous() {
	for _, m := range r.mirrorRoots {
		r.mirrorDir(m.srcFS, m.dest, m.src)
	}
}

func (r *runner) mirrorDir(srcFS vfs.FS, destDir, srcDir string) {
	entries, err := r.dfs.ReadDir(destDir)
	if err != nil {
		return // destDir isn't a directory (e.g. a file source) or doesn't exist
	}
	removed := false
	for _, e := range entries {
		destPath := r.dfs.Join(destDir, e.Name())
		srcPath := srcFS.Join(srcDir, e.Name())
		if _, err := srcFS.Lstat(srcPath); err != nil {
			// No source counterpart -> extraneous; remove it.
			if r.opts.DryRun {
				r.deleted.Add(1)
				r.item("would delete %s", r.dfs.Display(destPath))
				continue
			}
			if err := r.dfs.RemoveAll(destPath); err != nil {
				r.fail(fmt.Errorf("delete %s: %w", r.dfs.Display(destPath), err))
				continue
			}
			removed = true
			r.deleted.Add(1)
			r.item("deleted %s", r.dfs.Display(destPath))
			continue
		}
		if e.IsDir() {
			r.mirrorDir(srcFS, destPath, srcPath)
		}
	}
	if removed && r.copyOpts.Preserve {
		// Deleting entries bumped destDir's mtime after the copy phase already
		// applied directory metadata; restore it from the source so --mirror
		// doesn't clobber preserved directory times.
		if fi, err := srcFS.Lstat(srcDir); err == nil {
			if err := fsx.ApplyMeta(srcFS, srcDir, r.dfs, destDir, fi, true); err != nil {
				r.warn("metadata on %s: %v", r.dfs.Display(destDir), err)
			}
		}
	}
}
