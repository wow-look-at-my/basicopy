// Package engine orchestrates a copy run: it resolves the destination, walks the
// sources (pipelined), and copies files concurrently through a resizable gate
// using the fsx primitives. Directory creation and symlink handling happen on the
// walk goroutine so a child's parent always exists before the child is scheduled;
// directory metadata is applied after all copies complete so file writes don't
// bump directory mtimes back.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/scan"
)

// Summary reports the outcome of a copy run. Failed > 0 should map to a non-zero
// process exit.
type Summary struct {
	Files    int64 `json:"files"`
	Dirs     int64 `json:"dirs"`
	Symlinks int64 `json:"symlinks"`
	Linked   int64 `json:"hardlinks"` // hardlinks preserved
	Deleted  int64 `json:"deleted"`   // extraneous destination entries removed (--mirror)
	Bytes    int64 `json:"bytes"`
	Skipped  int64 `json:"skipped"`
	Failed   int64 `json:"failed"`
}

// Run executes the copy described by opts, returning a Summary even on partial
// failure.
func Run(ctx context.Context, opts *options.Options) (*Summary, error) {
	maxW, initW := workerCount(opts)
	r := &runner{
		opts:        opts,
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		onStack:     map[string]bool{},
		hardlinkMap: map[string]string{},
		gate:        newGate(maxW, initW),
		jobs:        make(chan fileJob, 1024),
		copyOpts: fsx.CopyOptions{
			Preserve: !opts.NoPreserve,
			Fsync:    opts.Fsync,
			BufSize:  int(opts.BufferSize),
		},
	}
	// Stream per-chunk byte progress into a live counter so the watchdog, progress
	// line, and autoscaler see a large file advancing mid-copy — not a flat line
	// until it finishes. Summary bytes stay sourced from completed copies (so a
	// retried file isn't double-counted); moved is monotonic and for liveness only.
	r.copyOpts.Progress = func(n int64) { r.moved.Add(n) }

	// Cancel the run if the context is cancelled (Ctrl-C); crash-safe temp files
	// are cleaned up by fsx.CopyFile's deferred removal.
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			r.setAbort(ctx.Err())
		case <-stopWatch:
		}
	}()

	stopBg := make(chan struct{})
	if !opts.DryRun {
		for i := 0; i < maxW; i++ {
			r.wg.Add(1)
			go r.worker()
		}
		r.bgWg.Add(3)
		go func() { defer r.bgWg.Done(); r.runController(ctx, stopBg) }()
		go func() { defer r.bgWg.Done(); r.runWatchdog(ctx, stopBg) }()
		go func() { defer r.bgWg.Done(); r.runProgress(ctx, stopBg) }()
	}

	err := r.walk(ctx)

	close(r.jobs)
	if !opts.DryRun {
		r.wg.Wait()
		close(stopBg)
		r.bgWg.Wait()
	}
	close(stopWatch)

	if err == nil {
		err = r.abortErr()
	}
	if !opts.DryRun {
		r.applyHardlinks()
		r.applyDirMeta()
	}
	if opts.Mirror {
		r.mirrorExtraneous()
	}
	return r.summary(), err
}

type fileJob struct {
	src, dst string
	info     os.FileInfo
}

type dirMetaEnt struct {
	dst  string
	info os.FileInfo
}

type runner struct {
	opts     *options.Options
	copyOpts fsx.CopyOptions

	gate *gate
	jobs chan fileJob
	wg   sync.WaitGroup // copy workers
	bgWg sync.WaitGroup // background goroutines: controller, watchdog, progress

	outMu  sync.Mutex
	stdout io.Writer
	stderr io.Writer

	files, dirs, symlinks, bytes, skipped, failed, linked, deleted atomic.Int64

	// moved is a monotonic count of bytes written across all in-flight and
	// completed copies (updated mid-copy via copyOpts.Progress). It is the
	// liveness/throughput signal for the watchdog, progress line, and autoscaler;
	// bytes (above) remains the authoritative completed-byte total for the summary.
	moved atomic.Int64

	abortMu sync.Mutex
	aborted error

	// Walk-goroutine-owned state (no locking needed): applied after wg.Wait.
	dirMeta     []dirMetaEnt
	onStack     map[string]bool
	hardlinkMap map[string]string // inode key -> first destination ("primary")
	hardlinks   []linkEnt         // secondary links to create after all copies
	rootDev     uint64            // device of the current source root (one-file-system)
	rootDevSet  bool
}

type linkEnt struct{ target, dst string }

// workerCount returns the gate ceiling and its initial limit. With --max-threads
// the count is pinned; otherwise it scales with CPU count (the M4 controller will
// drive the live limit between 1 and the ceiling).
func workerCount(opts *options.Options) (max, initial int) {
	max = opts.MaxThreads
	if max <= 0 {
		max = runtime.NumCPU() * 8
		if max < 8 {
			max = 8
		}
		if max > 256 {
			max = 256
		}
	}
	// A neutral starting limit for the brief window before the controller's first
	// tick (and for copies too short for it to engage); the controller resizes
	// from here based on device class and measured throughput.
	initial = 2 * runtime.NumCPU()
	if initial < 4 {
		initial = 4
	}
	if initial > max {
		initial = max
	}
	return max, initial
}

func (r *runner) worker() {
	defer r.wg.Done()
	for job := range r.jobs {
		if r.abortErr() != nil {
			continue // drain the channel without doing work
		}
		r.gate.acquire()
		r.copyOne(job)
		r.gate.release()
	}
}

// Retry tunables (vars so tests can shorten them).
var (
	maxRetries     = 3
	retryBaseDelay = 100 * time.Millisecond
)

func retryBackoff(attempt int) time.Duration {
	d := retryBaseDelay << attempt
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

// copyOne performs a single file copy and records stats, retrying transient
// errors a few times with short backoff. Safe for concurrent use.
func (r *runner) copyOne(job fileJob) {
	var (
		n   int64
		err error
	)
	for attempt := 0; ; attempt++ {
		n, err = fsx.CopyFile(job.src, job.dst, job.info, r.copyOpts)
		if err == nil || attempt >= maxRetries || !retryable(err) || r.abortErr() != nil {
			break
		}
		time.Sleep(retryBackoff(attempt))
	}
	if err != nil {
		r.fail(err)
		return
	}
	r.files.Add(1)
	r.bytes.Add(n)
	r.verbose("%s", job.dst)
}

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
	r.setRootDev(fi)
	rootAbs, _ := filepath.Abs(filepath.Dir(src))
	r.visit(ctx, src, r.opts.TargetFile, rootAbs, fi)
	return nil
}

func (r *runner) walkTargetDir(ctx context.Context) error {
	if err := r.ensureRoot(r.opts.TargetDir); err != nil {
		return err
	}
	for _, src := range r.opts.Sources {
		if r.abortErr() != nil {
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
		r.setRootDev(fi)
		rootAbs, _ := filepath.Abs(clean)
		r.visit(ctx, src, filepath.Join(r.opts.TargetDir, base), rootAbs, fi)
	}
	return nil
}

// visit dispatches a single entry. Directories and symlinks are handled on the
// walk goroutine; regular files are queued for the worker pool.
func (r *runner) visit(ctx context.Context, srcPath, dstPath, srcRootAbs string, fi os.FileInfo) {
	if r.abortErr() != nil {
		return
	}
	select {
	case <-ctx.Done():
		r.setAbort(ctx.Err())
		return
	default:
	}

	if r.excluded(srcPath, srcRootAbs) {
		r.verbose("exclude %s", srcPath)
		return
	}

	mode := fi.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		r.visitSymlink(ctx, srcPath, dstPath, srcRootAbs, fi)
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
		r.handleRegular(srcPath, dstPath, fi)
	default:
		r.note("skipping special file %s", srcPath)
		r.skipped.Add(1)
	}
}

// handleRegular preserves hardlinks (default-on): the first path to a multiply-
// linked inode is copied; subsequent paths are recorded to be link()ed to that
// first copy after all copies finish. Single-link files are just copied.
func (r *runner) handleRegular(srcPath, dstPath string, fi os.FileInfo) {
	if scan.Unchanged(srcPath, fi, dstPath, r.opts.Checksum) {
		r.skipped.Add(1)
		r.verbose("skip unchanged %s", dstPath)
		return
	}
	if !r.opts.NoHardlinks && !r.opts.DryRun {
		if key, nlink, ok := fileKey(fi); ok && nlink > 1 {
			if primary, seen := r.hardlinkMap[key]; seen {
				r.hardlinks = append(r.hardlinks, linkEnt{target: primary, dst: dstPath})
				return
			}
			r.hardlinkMap[key] = dstPath
		}
	}
	r.enqueueFile(srcPath, dstPath, fi)
}

// applyHardlinks creates the recorded secondary hardlinks after all primary
// copies have completed.
func (r *runner) applyHardlinks() {
	for _, l := range r.hardlinks {
		_ = os.Remove(l.dst) // tolerate a leftover from a prior run
		if err := os.Link(l.target, l.dst); err != nil {
			r.fail(fmt.Errorf("hardlink %s -> %s: %w", l.dst, l.target, err))
			continue
		}
		r.linked.Add(1)
		r.verbose("%s => %s (hardlink)", l.dst, l.target)
	}
}

func (r *runner) visitDir(ctx context.Context, srcDir, dstDir, srcRootAbs string, fi os.FileInfo) {
	if !r.opts.DryRun {
		if err := os.MkdirAll(dstDir, 0o777); err != nil {
			r.fail(fmt.Errorf("mkdir %s: %w", dstDir, err))
			return
		}
		r.dirMeta = append(r.dirMeta, dirMetaEnt{dst: dstDir, info: fi})
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		r.fail(fmt.Errorf("read dir %s: %w", srcDir, err))
		return
	}
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
		r.visit(ctx, filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()), srcRootAbs, cfi)
	}
}

func (r *runner) enqueueFile(srcPath, dstPath string, fi os.FileInfo) {
	if r.opts.DryRun {
		r.files.Add(1)
		r.bytes.Add(fi.Size())
		r.verbose("would copy %s", dstPath)
		return
	}
	r.jobs <- fileJob{src: srcPath, dst: dstPath, info: fi}
}

func (r *runner) visitSymlink(ctx context.Context, srcPath, dstPath, srcRootAbs string, fi os.FileInfo) {
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
		r.enqueueFile(srcPath, dstPath, tfi)
	default:
		r.note("skipping special symlink target %s", srcPath)
		r.skipped.Add(1)
	}
}

func (r *runner) recreateSymlink(target, dstPath string, fi os.FileInfo) {
	if r.opts.DryRun {
		r.symlinks.Add(1)
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
	r.symlinks.Add(1)
	r.verbose("%s -> %s", dstPath, target)
}

// applyDirMeta sets directory metadata after all file copies, so additions to a
// directory during the run don't override its restored mtime.
func (r *runner) applyDirMeta() {
	for _, d := range r.dirMeta {
		if err := fsx.ApplyMeta(d.dst, d.info, r.copyOpts.Preserve); err != nil {
			r.warn("metadata on %s: %v", d.dst, err)
		}
	}
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

func (r *runner) setAbort(err error) {
	if err == nil {
		return
	}
	r.abortMu.Lock()
	if r.aborted == nil {
		r.aborted = err
	}
	r.abortMu.Unlock()
}

func (r *runner) abortErr() error {
	r.abortMu.Lock()
	defer r.abortMu.Unlock()
	return r.aborted
}

func (r *runner) fail(err error) {
	if err == nil {
		return
	}
	r.failed.Add(1)
	r.elog("basicopy: error: %v", err)
	if r.opts.FatalErrors {
		r.setAbort(err)
	}
}

func (r *runner) summary() *Summary {
	return &Summary{
		Files:    r.files.Load(),
		Dirs:     r.dirs.Load(),
		Symlinks: r.symlinks.Load(),
		Linked:   r.linked.Load(),
		Deleted:  r.deleted.Load(),
		Bytes:    r.bytes.Load(),
		Skipped:  r.skipped.Load(),
		Failed:   r.failed.Load(),
	}
}

func (r *runner) elog(format string, a ...any) {
	r.outMu.Lock()
	fmt.Fprintf(r.stderr, format+"\n", a...)
	r.outMu.Unlock()
}

func (r *runner) warn(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	r.elog("basicopy: warning: "+format, a...)
}

func (r *runner) note(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	r.elog("basicopy: "+format, a...)
}

func (r *runner) verbose(format string, a ...any) {
	if !r.opts.Verbose {
		return
	}
	r.outMu.Lock()
	fmt.Fprintf(r.stdout, format+"\n", a...)
	r.outMu.Unlock()
}

// mirrorExtraneous deletes destination entries that have no counterpart in the
// source (the --mirror / robocopy /MIR behavior). It runs after copying so the
// destination already reflects the source's content.
func (r *runner) mirrorExtraneous() {
	for _, src := range r.opts.Sources {
		base := filepath.Base(filepath.Clean(src))
		r.mirrorDir(filepath.Join(r.opts.TargetDir, base), src)
	}
}

func (r *runner) mirrorDir(destDir, srcDir string) {
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return // destDir isn't a directory (e.g. a file source) or doesn't exist
	}
	for _, e := range entries {
		destPath := filepath.Join(destDir, e.Name())
		srcPath := filepath.Join(srcDir, e.Name())
		if _, err := os.Lstat(srcPath); err != nil {
			// No source counterpart -> extraneous; remove it.
			if r.opts.DryRun {
				r.deleted.Add(1)
				r.verbose("would delete %s", destPath)
				continue
			}
			if err := os.RemoveAll(destPath); err != nil {
				r.fail(fmt.Errorf("delete %s: %w", destPath, err))
				continue
			}
			r.deleted.Add(1)
			r.verbose("deleted %s", destPath)
			continue
		}
		if e.IsDir() {
			r.mirrorDir(destPath, srcPath)
		}
	}
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
// against both the path relative to the source root and the basename.
func (r *runner) excluded(srcPath, srcRootAbs string) bool {
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
		if matchGlob(inc, rel) || matchGlob(inc, base) {
			return false
		}
	}
	for _, exc := range r.opts.Exclude {
		if matchGlob(exc, rel) || matchGlob(exc, base) {
			return true
		}
	}
	return false
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
