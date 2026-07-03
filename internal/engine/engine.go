// Package engine orchestrates a copy run: it resolves the destination, walks the
// sources (pipelined), and copies files concurrently through a resizable gate
// using the fsx primitives. Directory creation and symlink handling happen on the
// walk goroutine so a child's parent always exists before the child is scheduled;
// directory metadata is applied once each directory's own child entries are done
// so file writes don't bump directory mtimes back.
package engine

import (
	"context"
	"errors"
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
	Updated  int64 `json:"updated"`   // attribute-only touch-ups on otherwise unchanged entries
	Deleted  int64 `json:"deleted"`   // extraneous destination entries removed (--mirror)
	Bytes    int64 `json:"bytes"`
	Skipped  int64 `json:"skipped"`
	Failed   int64 `json:"failed"`
}

// Output destinations for per-item lines and diagnostics (vars so tests can
// capture a full run's output).
var (
	runStdout io.Writer = os.Stdout
	runStderr io.Writer = os.Stderr
)

// Run executes the copy described by opts, returning a Summary even on partial
// failure.
func Run(ctx context.Context, opts *options.Options) (*Summary, error) {
	maxW, initW := workerCount(opts)
	r := &runner{
		opts:        opts,
		stdout:      runStdout,
		stderr:      runStderr,
		startedAt:   time.Now(),
		onStack:     map[string]bool{},
		hardlinkMap: map[string]hlPrimary{},
		gate:        newGate(maxW, initW),
		jobs:        make(chan fileJob, 1024),
		metaJobs:    make(chan *dirState, 1024),
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
	// Stop in-flight copies at their next chunk boundary once the run is
	// aborting (Ctrl-C, watchdog, --fatal-errors, out of space) — without this a
	// cancelled run would still finish every in-flight file, which for large
	// files can take minutes.
	r.copyOpts.Cancel = func() bool { return r.abortErr() != nil }

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
		for i := 0; i < metadataWorkerCount(); i++ {
			r.metaWg.Add(1)
			go r.metadataWorker()
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
	}
	close(stopWatch)

	r.applyHardlinks()
	if !opts.DryRun {
		close(r.metaJobs)
		r.metaWg.Wait()
	}
	if opts.Mirror {
		r.mirrorExtraneous()
	}
	if !opts.DryRun {
		close(stopBg)
		r.bgWg.Wait()
	}
	if err == nil {
		err = r.abortErr()
	}
	return r.summary(), err
}

type fileJob struct {
	src, dst string
	info     os.FileInfo
	parent   *dirState

	// checkUnchanged defers the skip-unchanged decision to the worker: the
	// walk goroutine enqueues without stat/hashing the destination so that,
	// with --checksum, content hashing runs across the pool instead of
	// serializing on the walk.
	checkUnchanged bool

	// reason is the itemizable cause of the copy when the walk already
	// compared the destination (checkUnchanged false); the worker fills it in
	// otherwise.
	reason string
}

type runner struct {
	opts     *options.Options
	copyOpts fsx.CopyOptions

	gate *gate
	jobs chan fileJob
	wg   sync.WaitGroup // copy workers

	metaJobs chan *dirState
	metaWg   sync.WaitGroup // directory metadata workers

	bgWg sync.WaitGroup // background goroutines: controller, watchdog, progress

	outMu     sync.Mutex
	stdout    io.Writer
	stderr    io.Writer
	startedAt time.Time

	files, dirs, symlinks, bytes, skipped, failed, linked, deleted, updated atomic.Int64
	dirMetaTotal, dirMetaDone                                               atomic.Int64

	// moved is a monotonic count of bytes written across all in-flight and
	// completed copies (updated mid-copy via copyOpts.Progress). It is the
	// liveness/throughput signal for the watchdog, progress line, and autoscaler;
	// bytes (above) remains the authoritative completed-byte total for the summary.
	moved atomic.Int64

	// totalFiles/totalBytes grow as the walk discovers copy work, allowing the
	// status line to show live total progress and ETA without blocking copying.
	totalFiles, totalBytes atomic.Int64

	// Pre-write space guard: freeBytes tracks the destination's remaining space
	// (seeded by initSpaceGuard via statfs, then decremented per file) so copyOne
	// can refuse a file that would not fit and stop the run before an ENOSPC write.
	// spaceCheck is false when the figure can't be read (the guard is then off).
	freeBytes  atomic.Int64
	spaceCheck bool

	abortMu sync.Mutex
	aborted error

	// Walk-goroutine-owned state (no locking needed).
	onStack     map[string]bool
	hardlinkMap map[string]hlPrimary // inode key -> primary destination
	hardlinks   []linkEnt            // secondary links to create after all copies
	mirrorRoots []mirrorRoot         // dest/src pairs the --mirror pass may prune
	rootDev     uint64               // device of the current source root (one-file-system)
	rootDevSet  bool
}

// mirrorRoot pairs a walked source root with its destination. The --mirror pass
// deletes only under roots recorded here, so a source that failed its safety
// guards (or didn't exist) can never have its destination -- or, in contents
// mode, itself -- swept as "extraneous".
type mirrorRoot struct{ dest, src string }

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
			r.completeDirWork(job.parent)
			continue // drain the channel without doing work
		}
		r.gate.acquire()
		r.copyOne(job)
		r.gate.release()
		r.completeDirWork(job.parent)
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
	reason := job.reason
	if job.checkUnchanged {
		v := scan.Compare(job.src, job.info, job.dst, r.opts.Checksum)
		if !v.NeedCopy {
			// Undo the discovery accounting: these bytes will never move, so
			// the live progress denominator and ETA must not keep counting them.
			r.totalFiles.Add(-1)
			r.totalBytes.Add(-job.info.Size())
			r.finishUnchangedFile(job.dst, job.info, v)
			return
		}
		reason = v.Reason
	}
	if !r.reserveSpace(job.dst, job.info.Size()) {
		return // destination is full; the run has been aborted
	}
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
		if errors.Is(err, fsx.ErrCanceled) {
			return // the run is aborting; an interrupted copy is not a per-file failure
		}
		r.fail(err)
		return
	}
	r.files.Add(1)
	r.bytes.Add(n)
	r.item("copy %s (%s)", job.dst, reason)
}

// finishUnchangedFile finalizes a destination file whose content is already up
// to date: when metadata preservation is on and the verdict reports attribute
// drift, the attributes are synced (or, in a dry run, itemized) and the entry
// counts as updated; otherwise it is a plain skip. Attribute application is
// best-effort, matching the directory-metadata policy (a warning, not a
// failure).
func (r *runner) finishUnchangedFile(dstPath string, srcInfo os.FileInfo, v scan.Verdict) {
	if r.copyOpts.Preserve {
		if diff := attrDiff(srcInfo, v); diff != "" {
			if !r.opts.DryRun {
				if err := fsx.SyncAttrs(dstPath, srcInfo, v.ModeDiff, v.OwnerDiff, v.TimeDiff); err != nil {
					r.warn("metadata on %s: %v", dstPath, err)
				}
			}
			r.updated.Add(1)
			r.item("%supdate %s (%s)", r.would(), dstPath, diff)
			return
		}
	}
	r.skipped.Add(1)
	r.verbose("skip unchanged %s", dstPath)
}

// attrDiff renders a verdict's attribute drift as the itemize suffix, e.g.
// "mode 0600 -> 0644, owner 1000:1000 -> 0:0, mtime". Parts appear in that
// fixed order and only when they differ; the result is empty when nothing does.
func attrDiff(srcInfo os.FileInfo, v scan.Verdict) string {
	var parts []string
	if v.ModeDiff {
		parts = append(parts, fmt.Sprintf("mode %04o -> %04o", v.DstInfo.Mode().Perm(), srcInfo.Mode().Perm()))
	}
	if v.OwnerDiff {
		parts = append(parts, fmt.Sprintf("owner %d:%d -> %d:%d", v.DstUID, v.DstGID, v.SrcUID, v.SrcGID))
	}
	if v.TimeDiff {
		parts = append(parts, "mtime")
	}
	return strings.Join(parts, ", ")
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
	switch {
	case isNoSpace(err):
		// The destination is full: every remaining file would fail the same way,
		// so stop the whole run now instead of grinding through the rest of the
		// tree and reporting thousands of identical ENOSPC failures.
		r.setAbort(fmt.Errorf("destination is out of space: %w", err))
	case r.opts.FatalErrors:
		r.setAbort(err)
	}
}

func (r *runner) summary() *Summary {
	return &Summary{
		Files:    r.files.Load(),
		Dirs:     r.dirs.Load(),
		Symlinks: r.symlinks.Load(),
		Linked:   r.linked.Load(),
		Updated:  r.updated.Load(),
		Deleted:  r.deleted.Load(),
		Bytes:    r.bytes.Load(),
		Skipped:  r.skipped.Load(),
		Failed:   r.failed.Load(),
	}
}

// printLine writes one line to w under the shared output lock, so stdout item
// lines and stderr diagnostics never tear mid-line.
func (r *runner) printLine(w io.Writer, format string, a ...any) {
	r.outMu.Lock()
	fmt.Fprintf(w, format+"\n", a...)
	r.outMu.Unlock()
}

func (r *runner) elog(format string, a ...any) {
	r.printLine(r.stderr, format, a...)
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

// verbose prints a low-interest per-entry line ("skip unchanged", "exclude")
// that only appears under --verbose, in dry and real runs alike. --json owns
// stdout for the machine-readable summary, so it suppresses these too.
func (r *runner) verbose(format string, a ...any) {
	if !r.opts.Verbose || r.opts.JSON {
		return
	}
	r.printLine(r.stdout, format, a...)
}

// item prints one per-entry action line (copy, update, mkdir, link, hardlink,
// delete). In a dry run these lines ARE the product, so they print by default;
// --quiet suppresses them, as does --json, which owns stdout for the
// machine-readable summary (human lines would corrupt the stream). In a real
// run they print under --verbose only.
func (r *runner) item(format string, a ...any) {
	if r.opts.Quiet || r.opts.JSON || (!r.opts.DryRun && !r.opts.Verbose) {
		return
	}
	r.printLine(r.stdout, format, a...)
}

// would returns the "would " itemize prefix in a dry run, empty otherwise, for
// action lines that share their format between dry and real runs.
func (r *runner) would() string {
	if r.opts.DryRun {
		return "would "
	}
	return ""
}

// mirrorExtraneous deletes destination entries that have no counterpart in the
// source (the --mirror / robocopy /MIR behavior). It runs after copying so the
// destination already reflects the source's content, and only under the roots
// the walk actually processed.
func (r *runner) mirrorExtraneous() {
	for _, m := range r.mirrorRoots {
		r.mirrorDir(m.dest, m.src)
	}
}

func (r *runner) mirrorDir(destDir, srcDir string) {
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return // destDir isn't a directory (e.g. a file source) or doesn't exist
	}
	removed := false
	for _, e := range entries {
		destPath := filepath.Join(destDir, e.Name())
		srcPath := filepath.Join(srcDir, e.Name())
		if _, err := os.Lstat(srcPath); err != nil {
			// No source counterpart -> extraneous; remove it.
			if r.opts.DryRun {
				r.deleted.Add(1)
				r.item("would delete %s", destPath)
				continue
			}
			if err := os.RemoveAll(destPath); err != nil {
				r.fail(fmt.Errorf("delete %s: %w", destPath, err))
				continue
			}
			removed = true
			r.deleted.Add(1)
			r.item("deleted %s", destPath)
			continue
		}
		if e.IsDir() {
			r.mirrorDir(destPath, srcPath)
		}
	}
	if removed && r.copyOpts.Preserve {
		// Deleting entries bumped destDir's mtime after the copy phase already
		// applied directory metadata; restore it from the source so --mirror
		// doesn't clobber preserved directory times.
		if fi, err := os.Lstat(srcDir); err == nil {
			if err := fsx.ApplyMeta(srcDir, destDir, fi, true); err != nil {
				r.warn("metadata on %s: %v", destDir, err)
			}
		}
	}
}
