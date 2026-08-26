// The --mirror pass: deleting destination entries that have no counterpart in
// the source (robocopy's /MIR). It runs after copying, so the destination
// already reflects the source's content.
package engine

import (
	"fmt"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// mirrorExtraneous runs --mirror: delete destination entries with no source
// counterpart, after copying, under the walked roots.
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
		// A delete bumps destDir's mtime after the copy phase set it; restore it
		// so --mirror does not clobber preserved directory times.
		if fi, err := srcFS.Lstat(srcDir); err == nil {
			if err := fsx.ApplyMeta(srcFS, srcDir, r.dfs, destDir, fi, true); err != nil {
				r.warn("metadata on %s: %v", r.dfs.Display(destDir), err)
			}
		}
	}
}
