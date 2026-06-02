// Package engine orchestrates a copy run: it resolves the destination, walks the
// sources, and copies each entry using the fsx primitives. This file implements a
// correct *sequential* copy (path semantics, auto-mkdir, metadata, and the
// symlink follow/keep/loop rules). Parallelism and the auto-scaling controller
// are layered on in later milestones without changing this observable behavior.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

// Summary reports the outcome of a copy run. A Failed count > 0 should map to a
// non-zero process exit.
type Summary struct {
	Files    int64
	Dirs     int64
	Symlinks int64
	Bytes    int64
	Skipped  int64
	Failed   int64
}

// Run executes the copy described by opts, returning a Summary even on partial
// failure.
func Run(ctx context.Context, opts *options.Options) (*Summary, error) {
	r := &runner{
		opts:    opts,
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		onStack: map[string]bool{},
		copyOpts: fsx.CopyOptions{
			Preserve: !opts.NoPreserve,
			Fsync:    opts.Fsync,
			BufSize:  int(opts.BufferSize),
		},
	}
	err := r.run(ctx)
	return &r.sum, err
}

type runner struct {
	opts     *options.Options
	copyOpts fsx.CopyOptions
	stdout   io.Writer
	stderr   io.Writer
	sum      Summary
	onStack  map[string]bool // canonical dirs reached via a followed symlink (loop guard)
	abort    error           // set when --fatal-errors trips or the context is cancelled
}

func (r *runner) run(ctx context.Context) error {
	if r.opts.TargetFile != "" {
		return r.runTargetFile(ctx)
	}
	return r.runTargetDir(ctx)
}

func (r *runner) runTargetFile(ctx context.Context) error {
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
	rootAbs, _ := filepath.Abs(filepath.Dir(src))
	r.copyEntry(ctx, src, r.opts.TargetFile, rootAbs, fi)
	return r.abort
}

func (r *runner) runTargetDir(ctx context.Context) error {
	if err := r.ensureRoot(r.opts.TargetDir); err != nil {
		return err
	}
	for _, src := range r.opts.Sources {
		if r.abort != nil {
			break
		}
		clean := filepath.Clean(src)
		base := filepath.Base(clean)
		if base == "." || base == ".." || base == string(filepath.Separator) {
			r.fail(fmt.Errorf("cannot derive a destination name for source %q", src))
			continue
		}
		fi, err := os.Lstat(src)
		if err != nil {
			r.fail(err)
			continue
		}
		rootAbs, _ := filepath.Abs(clean)
		r.copyEntry(ctx, src, filepath.Join(r.opts.TargetDir, base), rootAbs, fi)
	}
	return r.abort
}

// copyEntry dispatches a single filesystem entry by type. srcRootAbs is the
// absolute root of the source currently being copied, used for in-tree symlink
// detection.
func (r *runner) copyEntry(ctx context.Context, srcPath, dstPath, srcRootAbs string, fi os.FileInfo) {
	if r.abort != nil {
		return
	}
	select {
	case <-ctx.Done():
		r.abort = ctx.Err()
		return
	default:
	}

	mode := fi.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		r.handleSymlink(ctx, srcPath, dstPath, srcRootAbs, fi)
	case mode.IsDir():
		r.copyDir(ctx, srcPath, dstPath, srcRootAbs, fi)
	case mode.IsRegular():
		r.copyRegular(srcPath, dstPath, fi)
	default:
		r.note("skipping special file %s", srcPath)
		r.sum.Skipped++
	}
}

func (r *runner) copyDir(ctx context.Context, srcDir, dstDir, srcRootAbs string, fi os.FileInfo) {
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
	r.sum.Dirs++
	for _, e := range entries {
		if r.abort != nil {
			return
		}
		cfi, err := e.Info()
		if err != nil {
			r.fail(err)
			continue
		}
		r.copyEntry(ctx, filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()), srcRootAbs, cfi)
	}
	// Apply directory metadata after children so their writes don't bump mtime.
	if !r.opts.DryRun {
		if err := fsx.ApplyMeta(dstDir, fi, r.copyOpts.Preserve); err != nil {
			r.warn("metadata on %s: %v", dstDir, err)
		}
	}
}

func (r *runner) copyRegular(srcPath, dstPath string, fi os.FileInfo) {
	if r.opts.DryRun {
		r.sum.Files++
		r.sum.Bytes += fi.Size()
		r.verbose("would copy %s", dstPath)
		return
	}
	n, err := fsx.CopyFile(srcPath, dstPath, fi, r.copyOpts)
	if err != nil {
		r.fail(err)
		return
	}
	r.sum.Files++
	r.sum.Bytes += n
	r.verbose("%s", dstPath)
}

func (r *runner) handleSymlink(ctx context.Context, srcPath, dstPath, srcRootAbs string, fi os.FileInfo) {
	target, err := os.Readlink(srcPath)
	if err != nil {
		r.fail(err)
		return
	}
	if r.opts.NoFollowSymlinks {
		r.recreateSymlink(target, dstPath, fi)
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
		r.recreateSymlink(target, dstPath, fi)
		return
	}
	if !withinRoot(srcRootAbs, targetAbs) {
		if !r.opts.NoSymlinkWarnings {
			r.warn("symlink %s -> %s points outside the source tree (kept as link)", srcPath, target)
		}
		r.recreateSymlink(target, dstPath, fi)
		return
	}

	// In-tree: dereference into real content.
	switch {
	case tfi.IsDir():
		canon, err := filepath.EvalSymlinks(srcPath)
		if err != nil {
			r.fail(err)
			return
		}
		if r.onStack[canon] {
			r.warn("symlink loop at %s (skipped)", srcPath)
			r.sum.Skipped++
			return
		}
		r.onStack[canon] = true
		r.copyDir(ctx, srcPath, dstPath, srcRootAbs, tfi)
		delete(r.onStack, canon)
	case tfi.Mode().IsRegular():
		r.copyRegular(srcPath, dstPath, tfi)
	default:
		r.note("skipping special symlink target %s", srcPath)
		r.sum.Skipped++
	}
}

func (r *runner) recreateSymlink(target, dstPath string, fi os.FileInfo) {
	if r.opts.DryRun {
		r.sum.Symlinks++
		r.verbose("would link %s -> %s", dstPath, target)
		return
	}
	_ = os.Remove(dstPath) // tolerate a leftover from a prior run
	if err := os.Symlink(target, dstPath); err != nil {
		r.fail(fmt.Errorf("symlink %s: %w", dstPath, err))
		return
	}
	if err := fsx.ApplyMeta(dstPath, fi, r.copyOpts.Preserve); err != nil {
		r.warn("metadata on %s: %v", dstPath, err)
	}
	r.sum.Symlinks++
	r.verbose("%s -> %s", dstPath, target)
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
			fmt.Fprintf(r.stderr, "basicopy: would create directory %s\n", missing[i])
			continue
		}
		if err := os.Mkdir(missing[i], 0o777); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create %s: %w", missing[i], err)
		}
		fmt.Fprintf(r.stderr, "basicopy: created directory %s\n", missing[i])
	}
	return nil
}

func (r *runner) fail(err error) {
	if err == nil {
		return
	}
	r.sum.Failed++
	fmt.Fprintf(r.stderr, "basicopy: error: %v\n", err)
	if r.opts.FatalErrors && r.abort == nil {
		r.abort = err
	}
}

func (r *runner) warn(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	fmt.Fprintf(r.stderr, "basicopy: warning: "+format+"\n", a...)
}

func (r *runner) note(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	fmt.Fprintf(r.stderr, "basicopy: "+format+"\n", a...)
}

func (r *runner) verbose(format string, a ...any) {
	if r.opts.Verbose {
		fmt.Fprintf(r.stdout, format+"\n", a...)
	}
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
