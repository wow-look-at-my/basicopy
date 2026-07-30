// Walking and per-entry dispatch: destination resolution for each source, the
// recursive visit of directories/files/symlinks on the single walk goroutine,
// per-entry itemization, exclude/include matching, and destination-root setup.
// Every source carries the filesystem it lives on (srcRoot), so a walk over an
// SSH connection is the same code as a walk over a local directory.
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/scan"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// srcRoot is the per-source walk context: the filesystem the source lives on and
// the absolute path of its root, against which exclude patterns are matched and
// symlink targets are tested for containment.
type srcRoot struct {
	fs      vfs.FS
	rootAbs string
}

func (r *runner) walk(ctx context.Context) error {
	if r.opts.TargetFile != "" {
		return r.walkTargetFile(ctx)
	}
	return r.walkTargetDir(ctx)
}

func (r *runner) walkTargetFile(ctx context.Context) error {
	sfs, src, err := r.reg.Resolve(r.opts.Sources[0])
	if err != nil {
		return err
	}
	fi, err := sfs.Lstat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("--target-file given but source %q is a directory", sfs.Display(src))
	}
	if err := r.ensureRoot(r.dfs.Dir(r.targetFile)); err != nil {
		return err
	}
	r.initSpaceGuard(r.dfs.Dir(r.targetFile))
	r.setRootDev(sfs, fi)
	rootAbs, _ := sfs.Abs(sfs.Dir(src))
	r.visit(ctx, &srcRoot{fs: sfs, rootAbs: rootAbs}, src, r.targetFile, fi, nil)
	return nil
}

func (r *runner) walkTargetDir(ctx context.Context) error {
	for _, arg := range r.opts.Sources {
		if r.abortErr() != nil {
			break
		}
		sfs, src, err := r.reg.Resolve(arg)
		if err != nil {
			return err
		}
		clean := sfs.Clean(src)
		fi, err := sfs.Lstat(src)
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
			dstPath = r.dfs.Clean(r.targetDir)
		} else {
			base := sfs.Base(clean)
			if base == "." || base == ".." || base == sfs.Separator() {
				r.fail(fmt.Errorf("cannot derive a destination name for source %q", sfs.Display(src)))
				continue
			}
			dstPath = r.dfs.Join(r.targetDir, base)
		}
		if fi.IsDir() && vfs.SameHost(sfs, r.dfs) {
			// Overlap guards only mean anything when both ends are the same
			// filesystem; /srv on this machine and /srv on another host are
			// unrelated paths that happen to spell the same.
			srcAbs, srcErr := sfs.Abs(clean)
			dstAbs, dstErr := r.dfs.Abs(dstPath)
			if srcErr == nil && dstErr == nil {
				if withinRoot(sfs, srcAbs, dstAbs) {
					if srcAbs == dstAbs {
						r.fail(fmt.Errorf("destination %q is the source itself", r.dfs.Display(dstPath)))
					} else {
						r.fail(fmt.Errorf("destination %q is inside source %q", r.dfs.Display(dstPath), sfs.Display(src)))
					}
					continue
				}
				// Note: in contents mode the target being the source's PARENT
				// is legal (rsync SRC/ PARENT/), so no withinRoot guard the
				// other way around -- except under --mirror, where the pass
				// would then delete the source tree itself as "extraneous".
				if contents && r.opts.Mirror && withinRoot(sfs, dstAbs, srcAbs) {
					r.fail(fmt.Errorf("--mirror with --contents would delete source %q inside target %q", sfs.Display(src), r.dfs.Display(dstPath)))
					continue
				}
			}
		}
		if err := r.ensureRoot(r.targetDir); err != nil {
			return err
		}
		if r.opts.Mirror {
			r.mirrorRoots = append(r.mirrorRoots, mirrorRoot{srcFS: sfs, dest: dstPath, src: clean})
		}
		r.initSpaceGuard(r.targetDir)
		r.setRootDev(sfs, fi)
		rootAbs, _ := sfs.Abs(clean)
		r.visit(ctx, &srcRoot{fs: sfs, rootAbs: rootAbs}, src, dstPath, fi, nil)
	}
	return nil
}

// visit dispatches a single entry. Directories and symlinks are handled on the
// walk goroutine; regular files are queued for the worker pool.
func (r *runner) visit(ctx context.Context, root *srcRoot, srcPath, dstPath string, fi os.FileInfo, parent *dirState) {
	if r.abortErr() != nil {
		return
	}
	select {
	case <-ctx.Done():
		r.setAbort(ctx.Err())
		return
	default:
	}

	if r.excluded(root, srcPath, fi.IsDir()) {
		r.verbose("exclude %s", root.fs.Display(srcPath))
		return
	}

	mode := fi.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		r.visitSymlink(ctx, root, srcPath, dstPath, fi, parent)
	case mode.IsDir():
		if r.opts.OneFileSystem && r.rootDevSet {
			if dev, ok := root.fs.Dev(fi); ok && dev != r.rootDev {
				r.note("skipping mount point %s", root.fs.Display(srcPath))
				r.skipped.Add(1)
				return
			}
		}
		r.visitDir(ctx, root, srcPath, dstPath, fi)
	case mode.IsRegular():
		r.handleRegular(root, srcPath, dstPath, fi, parent)
	default:
		r.note("skipping special file %s", root.fs.Display(srcPath))
		r.skipped.Add(1)
	}
}

func (r *runner) visitDir(ctx context.Context, root *srcRoot, srcDir, dstDir string, fi os.FileInfo) {
	r.noteDirChange(root, srcDir, dstDir, fi)
	if !r.opts.DryRun {
		if err := r.dfs.MkdirAll(dstDir, 0o777); err != nil {
			r.fail(fmt.Errorf("mkdir %s: %w", r.dfs.Display(dstDir), err))
			return
		}
	}
	entries, err := root.fs.ReadDir(srcDir)
	if err != nil {
		r.fail(fmt.Errorf("read dir %s: %w", root.fs.Display(srcDir), err))
		return
	}
	state := r.newDirState(root.fs, srcDir, dstDir, fi)
	defer r.closeDir(state)
	r.dirs.Add(1)
	for _, cfi := range entries {
		if r.abortErr() != nil {
			return
		}
		r.visit(ctx, root, root.fs.Join(srcDir, cfi.Name()), r.dfs.Join(dstDir, cfi.Name()), cfi, state)
	}
}

// noteDirChange itemizes what happens to a destination directory: "mkdir" when
// it doesn't exist yet, or an attribute-only "update" when it exists with
// drifted mode/owner (never a pure-mtime line -- that is rsync's noisiest,
// least useful output). The Lstat behind the answer is paid only on paths that
// report (every dry run, and real runs under --verbose); the quiet real-run
// fast path stays stat-free, so directory updates are not counted there (the
// metadata pass re-applies directory attributes in real runs regardless).
func (r *runner) noteDirChange(root *srcRoot, srcDir, dstDir string, fi os.FileInfo) {
	if !r.opts.DryRun && !r.opts.Verbose {
		return
	}
	di, err := r.dfs.Lstat(dstDir)
	if err != nil || !di.IsDir() {
		r.item("%smkdir %s", r.would(), r.dfs.Display(dstDir))
		return
	}
	if !r.copyOpts.Preserve {
		return
	}
	if diff := attrDiff(fi, scan.CompareAttrs(root.fs, fi, r.dfs, di)); diff != "" {
		r.updated.Add(1)
		r.item("%supdate %s (%s)", r.would(), r.dfs.Display(dstDir), diff)
	}
}

func (r *runner) enqueueFile(root *srcRoot, srcPath, dstPath string, fi os.FileInfo, parent *dirState, checkUnchanged bool, reason string) {
	r.totalFiles.Add(1)
	r.totalBytes.Add(fi.Size())
	if r.opts.DryRun {
		r.files.Add(1)
		r.bytes.Add(fi.Size())
		r.item("would copy %s (%s)", r.dfs.Display(dstPath), reason)
		return
	}
	r.addDirWork(parent)
	r.jobs <- fileJob{srcFS: root.fs, src: srcPath, dst: dstPath, info: fi, parent: parent, checkUnchanged: checkUnchanged, reason: reason}
}

func (r *runner) visitSymlink(ctx context.Context, root *srcRoot, srcPath, dstPath string, fi os.FileInfo, parent *dirState) {
	target, err := root.fs.Readlink(srcPath)
	if err != nil {
		r.fail(err)
		return
	}
	if r.opts.NoFollowSymlinks {
		r.recreateSymlink(root, srcPath, target, dstPath, fi)
		return
	}

	targetAbs := target
	if !root.fs.IsAbs(target) {
		targetAbs = root.fs.Join(root.fs.Dir(srcPath), target)
	}
	targetAbs = root.fs.Clean(targetAbs)
	if abs, err := root.fs.Abs(targetAbs); err == nil {
		targetAbs = abs
	}

	tfi, statErr := root.fs.Stat(srcPath) // follows the link
	if statErr != nil {
		r.warn("dangling symlink %s -> %s (kept as link)", root.fs.Display(srcPath), target)
		r.recreateSymlink(root, srcPath, target, dstPath, fi)
		return
	}
	if !withinRoot(root.fs, root.rootAbs, targetAbs) {
		if !r.opts.NoSymlinkWarnings {
			r.warn("symlink %s -> %s points outside the source tree (kept as link)", root.fs.Display(srcPath), target)
		}
		r.recreateSymlink(root, srcPath, target, dstPath, fi)
		return
	}

	switch {
	case tfi.IsDir():
		canon, err := canonicalDir(root.fs, srcPath, targetAbs)
		if err != nil {
			r.fail(err)
			return
		}
		key := root.fs.Display(canon)
		if r.onStack[key] {
			r.warn("symlink loop at %s (skipped)", root.fs.Display(srcPath))
			r.skipped.Add(1)
			return
		}
		r.onStack[key] = true
		r.visitDir(ctx, root, srcPath, dstPath, tfi)
		delete(r.onStack, key)
	case tfi.Mode().IsRegular():
		// Route through handleRegular (not enqueueFile directly) so a
		// dereferenced symlink target gets the same skip-unchanged check and
		// hardlink identity handling as any other regular file -- otherwise an
		// incremental re-run recopies every in-tree symlink target forever.
		r.handleRegular(root, srcPath, dstPath, tfi, parent)
	default:
		r.note("skipping special symlink target %s", root.fs.Display(srcPath))
		r.skipped.Add(1)
	}
}

// canonicalDir returns a stable identity for a symlinked directory, used to
// detect loops. Locally that is the fully resolved path; remotely there is no
// realpath-through-every-component call worth a round trip per link, so the
// resolved target path stands in -- enough to catch a link that points at an
// ancestor, which is what a loop is.
func canonicalDir(fsys vfs.FS, srcPath, targetAbs string) (string, error) {
	if fsys.Local() {
		return filepath.EvalSymlinks(srcPath)
	}
	return targetAbs, nil
}

func (r *runner) recreateSymlink(root *srcRoot, srcPath, target, dstPath string, fi os.FileInfo) {
	// An existing symlink that already points at the same target is a skip,
	// matching the skip-unchanged treatment of regular files.
	if cur, err := r.dfs.Readlink(dstPath); err == nil && cur == target {
		r.skipped.Add(1)
		r.verbose("skip unchanged %s", r.dfs.Display(dstPath))
		return
	}
	if r.opts.DryRun {
		r.symlinks.Add(1)
		r.item("would link %s -> %s", r.dfs.Display(dstPath), target)
		return
	}
	_ = r.dfs.Remove(dstPath) // tolerate a leftover from a prior run
	if err := r.dfs.Symlink(target, dstPath); err != nil {
		r.fail(fmt.Errorf("symlink %s: %w", r.dfs.Display(dstPath), err))
		return
	}
	if err := fsx.ApplyMeta(root.fs, srcPath, r.dfs, dstPath, fi, r.copyOpts.Preserve); err != nil {
		r.warn("metadata on %s: %v", r.dfs.Display(dstPath), err)
	}
	r.symlinks.Add(1)
	r.item("link %s -> %s", r.dfs.Display(dstPath), target)
}

// ensureRoot makes the destination root exist, creating missing ancestors and
// announcing each created directory on stderr (unless --no-auto-mkdirs).
func (r *runner) ensureRoot(dir string) error {
	dir = r.dfs.Clean(dir)
	if fi, err := r.dfs.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("target %s exists but is not a directory", r.dfs.Display(dir))
		}
		return nil
	}
	if r.opts.NoAutoMkdirs {
		return fmt.Errorf("target directory %s does not exist (remove --no-auto-mkdirs to create it)", r.dfs.Display(dir))
	}

	var missing []string
	for p := dir; ; {
		if _, err := r.dfs.Stat(p); err == nil {
			break
		}
		missing = append(missing, p)
		parent := r.dfs.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if r.opts.DryRun {
			r.elog("basicopy: would create directory %s", r.dfs.Display(missing[i]))
			continue
		}
		if err := r.dfs.Mkdir(missing[i], 0o777); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create %s: %w", r.dfs.Display(missing[i]), err)
		}
		r.elog("basicopy: created directory %s", r.dfs.Display(missing[i]))
	}
	return nil
}

func (r *runner) setRootDev(fsys vfs.FS, fi os.FileInfo) {
	if dev, ok := fsys.Dev(fi); ok {
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
func (r *runner) excluded(root *srcRoot, srcPath string, isDir bool) bool {
	if len(r.opts.Exclude) == 0 && len(r.opts.Include) == 0 {
		return false
	}
	base := root.fs.Base(srcPath)
	rel := base
	if abs, err := root.fs.Abs(srcPath); err == nil {
		if rp, err := root.fs.Rel(root.rootAbs, abs); err == nil {
			rel = rp
		}
	}
	for _, inc := range r.opts.Include {
		if matchEntry(root.fs, inc, rel, base, isDir) {
			return false
		}
	}
	for _, exc := range r.opts.Exclude {
		if matchEntry(root.fs, exc, rel, base, isDir) {
			return true
		}
	}
	return false
}

// matchEntry applies one --exclude/--include pattern to an entry. A pattern
// ending in '/' matches directories only (rsync's 'node_modules/' syntax): the
// trailing slash(es) are stripped for the glob match and non-directories never
// match. Patterns without a trailing slash match files and directories alike.
func matchEntry(fsys vfs.FS, pattern, rel, base string, isDir bool) bool {
	if trimmed := strings.TrimRight(pattern, "/"); trimmed != pattern {
		if !isDir || trimmed == "" {
			return false
		}
		pattern = trimmed
	}
	return matchGlob(fsys, pattern, rel) || matchGlob(fsys, pattern, base)
}

func matchGlob(fsys vfs.FS, pattern, name string) bool {
	ok, err := fsys.Match(pattern, name)
	return err == nil && ok
}

// withinRoot reports whether targetAbs lies at or beneath rootAbs on fsys.
func withinRoot(fsys vfs.FS, rootAbs, targetAbs string) bool {
	rel, err := fsys.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+fsys.Separator())
}
