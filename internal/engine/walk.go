// Walking and per-entry dispatch: destination resolution for each source, the
// recursive visit of directories/files/symlinks on the single walk goroutine,
// per-entry itemization, exclude/include matching, and destination-root setup.
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/scan"
)

func (r *runner) walk(ctx context.Context) error {
	if r.opts.TargetFile != "" {
		return r.walkTargetFile(ctx)
	}
	return r.walkTargetDir(ctx)
}

func (r *runner) walkTargetFile(ctx context.Context) error {
	src := r.opts.Sources[0]
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("--target-file given but source %q is a directory", src)
	}
	if err := r.ensureRoot(filepath.Dir(r.opts.TargetFile)); err != nil {
		return err
	}
	r.initSpaceGuard(filepath.Dir(r.opts.TargetFile))
	r.setRootDev(fi)
	rootAbs, _ := filepath.Abs(filepath.Dir(src))
	r.visit(ctx, src, r.opts.TargetFile, rootAbs, fi, nil)
	return nil
}

func (r *runner) walkTargetDir(ctx context.Context) error {
	for _, src := range r.opts.Sources {
		if r.abortErr() != nil {
			break
		}
		clean := filepath.Clean(src)
		fi, err := os.Lstat(src)
		if err != nil {
			r.fail(err)
			continue
		}
		// With --contents a directory source's entries land directly in the
		// target directory (rsync's SRC/ trailing-slash semantics); otherwise
		// (and always for file sources) the source nests under its basename.
		contents := r.opts.Contents && fi.IsDir()
		var dstPath string
		if contents {
			dstPath = filepath.Clean(r.opts.TargetDir)
		} else {
			base := filepath.Base(clean)
			if base == "." || base == ".." || base == string(filepath.Separator) {
				r.fail(fmt.Errorf("cannot derive a destination name for source %q", src))
				continue
			}
			dstPath = filepath.Join(r.opts.TargetDir, base)
		}
		if fi.IsDir() {
			srcAbs, srcErr := filepath.Abs(clean)
			dstAbs, dstErr := filepath.Abs(dstPath)
			if srcErr == nil && dstErr == nil {
				if withinRoot(srcAbs, dstAbs) {
					if srcAbs == dstAbs {
						r.fail(fmt.Errorf("destination %q is the source itself", dstPath))
					} else {
						r.fail(fmt.Errorf("destination %q is inside source %q", dstPath, src))
					}
					continue
				}
				// Note: in contents mode the target being the source's PARENT
				// is legal (rsync SRC/ PARENT/), so no withinRoot guard the
				// other way around -- except under --mirror, where the pass
				// would then delete the source tree itself as "extraneous".
				if contents && r.opts.Mirror && withinRoot(dstAbs, srcAbs) {
					r.fail(fmt.Errorf("--mirror with --contents would delete source %q inside target %q", src, dstPath))
					continue
				}
			}
		}
		if err := r.ensureRoot(r.opts.TargetDir); err != nil {
			return err
		}
		if r.opts.Mirror {
			r.mirrorRoots = append(r.mirrorRoots, mirrorRoot{dest: dstPath, src: clean})
		}
		r.initSpaceGuard(r.opts.TargetDir)
		r.setRootDev(fi)
		rootAbs, _ := filepath.Abs(clean)
		r.visit(ctx, src, dstPath, rootAbs, fi, nil)
	}
	return nil
}

// visit dispatches a single entry. Directories and symlinks are handled on the
// walk goroutine; regular files are queued for the worker pool.
func (r *runner) visit(ctx context.Context, srcPath, dstPath, srcRootAbs string, fi os.FileInfo, parent *dirState) {
	if r.abortErr() != nil {
		return
	}
	select {
	case <-ctx.Done():
		r.setAbort(ctx.Err())
		return
	default:
	}

	if r.excluded(srcPath, srcRootAbs, fi.IsDir()) {
		r.verbose("exclude %s", srcPath)
		return
	}

	mode := fi.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		r.visitSymlink(ctx, srcPath, dstPath, srcRootAbs, fi, parent)
	case mode.IsDir():
		if r.opts.OneFileSystem && r.rootDevSet {
			if dev, ok := fileDev(fi); ok && dev != r.rootDev {
				r.note("skipping mount point %s", srcPath)
				r.skipped.Add(1)
				return
			}
		}
		r.visitDir(ctx, srcPath, dstPath, srcRootAbs, fi)
	case mode.IsRegular():
		r.handleRegular(srcPath, dstPath, fi, parent)
	default:
		r.note("skipping special file %s", srcPath)
		r.skipped.Add(1)
	}
}

func (r *runner) visitDir(ctx context.Context, srcDir, dstDir, srcRootAbs string, fi os.FileInfo) {
	r.noteDirChange(srcDir, dstDir, fi)
	if !r.opts.DryRun {
		if err := os.MkdirAll(dstDir, 0o777); err != nil {
			r.fail(fmt.Errorf("mkdir %s: %w", dstDir, err))
			return
		}
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		r.fail(fmt.Errorf("read dir %s: %w", srcDir, err))
		return
	}
	state := r.newDirState(srcDir, dstDir, fi)
	defer r.closeDir(state)
	r.dirs.Add(1)
	for _, e := range entries {
		if r.abortErr() != nil {
			return
		}
		cfi, err := e.Info()
		if err != nil {
			r.fail(err)
			continue
		}
		r.visit(ctx, filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()), srcRootAbs, cfi, state)
	}
}

// noteDirChange itemizes what happens to a destination directory: "mkdir" when
// it doesn't exist yet, or an attribute-only "update" when it exists with
// drifted mode/owner (never a pure-mtime line -- that is rsync's noisiest,
// least useful output). The Lstat behind the answer is paid only on paths that
// report (every dry run, and real runs under --verbose); the quiet real-run
// fast path stays stat-free, so directory updates are not counted there (the
// metadata pass re-applies directory attributes in real runs regardless).
func (r *runner) noteDirChange(srcDir, dstDir string, fi os.FileInfo) {
	if !r.opts.DryRun && !r.opts.Verbose {
		return
	}
	di, err := os.Lstat(dstDir)
	if err != nil || !di.IsDir() {
		r.item("%smkdir %s", r.would(), dstDir)
		return
	}
	if !r.copyOpts.Preserve {
		return
	}
	if diff := attrDiff(fi, scan.CompareAttrs(fi, di)); diff != "" {
		r.updated.Add(1)
		r.item("%supdate %s (%s)", r.would(), dstDir, diff)
	}
}

func (r *runner) enqueueFile(srcPath, dstPath string, fi os.FileInfo, parent *dirState, checkUnchanged bool, reason string) {
	r.totalFiles.Add(1)
	r.totalBytes.Add(fi.Size())
	if r.opts.DryRun {
		r.files.Add(1)
		r.bytes.Add(fi.Size())
		r.item("would copy %s (%s)", dstPath, reason)
		return
	}
	r.addDirWork(parent)
	r.jobs <- fileJob{src: srcPath, dst: dstPath, info: fi, parent: parent, checkUnchanged: checkUnchanged, reason: reason}
}

func (r *runner) visitSymlink(ctx context.Context, srcPath, dstPath, srcRootAbs string, fi os.FileInfo, parent *dirState) {
	target, err := os.Readlink(srcPath)
	if err != nil {
		r.fail(err)
		return
	}
	if r.opts.NoFollowSymlinks {
		r.recreateSymlink(srcPath, target, dstPath, fi)
		return
	}

	targetAbs := target
	if !filepath.IsAbs(target) {
		targetAbs = filepath.Join(filepath.Dir(srcPath), target)
	}
	targetAbs = filepath.Clean(targetAbs)
	if abs, err := filepath.Abs(targetAbs); err == nil {
		targetAbs = abs
	}

	tfi, statErr := os.Stat(srcPath) // follows the link
	if statErr != nil {
		r.warn("dangling symlink %s -> %s (kept as link)", srcPath, target)
		r.recreateSymlink(srcPath, target, dstPath, fi)
		return
	}
	if !withinRoot(srcRootAbs, targetAbs) {
		if !r.opts.NoSymlinkWarnings {
			r.warn("symlink %s -> %s points outside the source tree (kept as link)", srcPath, target)
		}
		r.recreateSymlink(srcPath, target, dstPath, fi)
		return
	}

	switch {
	case tfi.IsDir():
		canon, err := filepath.EvalSymlinks(srcPath)
		if err != nil {
			r.fail(err)
			return
		}
		if r.onStack[canon] {
			r.warn("symlink loop at %s (skipped)", srcPath)
			r.skipped.Add(1)
			return
		}
		r.onStack[canon] = true
		r.visitDir(ctx, srcPath, dstPath, srcRootAbs, tfi)
		delete(r.onStack, canon)
	case tfi.Mode().IsRegular():
		// Route through handleRegular (not enqueueFile directly) so a
		// dereferenced symlink target gets the same skip-unchanged check and
		// hardlink identity handling as any other regular file -- otherwise an
		// incremental re-run recopies every in-tree symlink target forever.
		r.handleRegular(srcPath, dstPath, tfi, parent)
	default:
		r.note("skipping special symlink target %s", srcPath)
		r.skipped.Add(1)
	}
}

func (r *runner) recreateSymlink(srcPath, target, dstPath string, fi os.FileInfo) {
	// An existing symlink that already points at the same target is a skip,
	// matching the skip-unchanged treatment of regular files.
	if cur, err := os.Readlink(dstPath); err == nil && cur == target {
		r.skipped.Add(1)
		r.verbose("skip unchanged %s", dstPath)
		return
	}
	if r.opts.DryRun {
		r.symlinks.Add(1)
		r.item("would link %s -> %s", dstPath, target)
		return
	}
	_ = os.Remove(dstPath) // tolerate a leftover from a prior run
	if err := os.Symlink(target, dstPath); err != nil {
		r.fail(fmt.Errorf("symlink %s: %w", dstPath, err))
		return
	}
	if err := fsx.ApplyMeta(srcPath, dstPath, fi, r.copyOpts.Preserve); err != nil {
		r.warn("metadata on %s: %v", dstPath, err)
	}
	r.symlinks.Add(1)
	r.item("link %s -> %s", dstPath, target)
}

// ensureRoot makes the destination root exist, creating missing ancestors and
// announcing each created directory on stderr (unless --no-auto-mkdirs).
func (r *runner) ensureRoot(dir string) error {
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("target %s exists but is not a directory", dir)
		}
		return nil
	}
	if r.opts.NoAutoMkdirs {
		return fmt.Errorf("target directory %s does not exist (remove --no-auto-mkdirs to create it)", dir)
	}

	var missing []string
	for p := dir; ; {
		if _, err := os.Stat(p); err == nil {
			break
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if r.opts.DryRun {
			r.elog("basicopy: would create directory %s", missing[i])
			continue
		}
		if err := os.Mkdir(missing[i], 0o777); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create %s: %w", missing[i], err)
		}
		r.elog("basicopy: created directory %s", missing[i])
	}
	return nil
}

func (r *runner) setRootDev(fi os.FileInfo) {
	if dev, ok := fileDev(fi); ok {
		r.rootDev, r.rootDevSet = dev, true
	} else {
		r.rootDevSet = false
	}
}

// excluded applies the --exclude/--include globs. A path is excluded if it
// matches any --exclude pattern and no --include pattern; patterns are matched
// against both the path relative to the source root and the basename. isDir
// says whether the entry is a directory, which patterns with a trailing slash
// require.
func (r *runner) excluded(srcPath, srcRootAbs string, isDir bool) bool {
	if len(r.opts.Exclude) == 0 && len(r.opts.Include) == 0 {
		return false
	}
	base := filepath.Base(srcPath)
	rel := base
	if abs, err := filepath.Abs(srcPath); err == nil {
		if rp, err := filepath.Rel(srcRootAbs, abs); err == nil {
			rel = rp
		}
	}
	for _, inc := range r.opts.Include {
		if matchEntry(inc, rel, base, isDir) {
			return false
		}
	}
	for _, exc := range r.opts.Exclude {
		if matchEntry(exc, rel, base, isDir) {
			return true
		}
	}
	return false
}

// matchEntry applies one --exclude/--include pattern to an entry. A pattern
// ending in '/' matches directories only (rsync's 'node_modules/' syntax): the
// trailing slash(es) are stripped for the glob match and non-directories never
// match. Patterns without a trailing slash match files and directories alike.
func matchEntry(pattern, rel, base string, isDir bool) bool {
	if trimmed := strings.TrimRight(pattern, "/"); trimmed != pattern {
		if !isDir || trimmed == "" {
			return false
		}
		pattern = trimmed
	}
	return matchGlob(pattern, rel) || matchGlob(pattern, base)
}

func matchGlob(pattern, name string) bool {
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}

// withinRoot reports whether targetAbs lies at or beneath rootAbs.
func withinRoot(rootAbs, targetAbs string) bool {
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
