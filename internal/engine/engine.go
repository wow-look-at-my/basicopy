// Package engine orchestrates a copy run. It resolves the destination, walks the
// sources (pipelined), and copies files concurrently through a resizable gate
// using the fsx primitives. Directory creation and symlink handling happen on the
// walk goroutine so a child's parent always exists before the child is scheduled.
// Directory metadata is applied a single time each directory's own child entries
// are done so file writes don't bump directory mtimes back.
//
// Either end may be a remote host written as "[user@]host:/path". Every
// filesystem call goes through the vfs seam. The walk, the worker pool, the
// controller, and the mirror pass are the same code for a local disk and for an
// SSH connection.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/scan"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// Summary reports the outcome of a copy run.
type Summary struct {
	Files    int64 `json:"files"`
	Dirs     int64 `json:"dirs"`
	Symlinks int64 `json:"symlinks"`
	Linked   int64 `json:"hardlinks"` // hardlinks preserved
	Updated  int64 `json:"updated"`   // attribute-only touch-ups on otherwise unchanged entries
	Deleted  int64 `json:"deleted"`   // extraneous destination entries removed (--mirror)
	Renamed  int64 `json:"renamed"`   // source roots moved by a single rename
	Removed  int64 `json:"removed"`   // source entries removed after their transfer (--move)
	Bytes    int64 `json:"bytes"`
	Cloned   int64 `json:"cloned"` // bytes cloned from an earlier copy of a shared source extent
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
		jobs:        newQueue[fileJob](),
		metaJobs:    newQueue[*dirState](),
		reg:         vfs.NewRegistry(),
		copyOpts: fsx.CopyOptions{
			Preserve: !opts.NoPreserve,
			Fsync:    opts.Fsync,
			BufSize:  int(opts.BufferSize),
			Dedup:    fsx.NewDedup(),
		},
	}
	r.reg.Diag = &lineWriter{r: r}
	defer r.reg.Close()
	if err := r.resolveTarget(); err != nil {
		return r.summary(), err
	}
	// Dial each remote source before the progress line starts: a first ssh connection can prompt on the terminal.
	for _, arg := range opts.Sources {
		if _, _, err := r.reg.Resolve(arg); err != nil {
			return r.summary(), err
		}
	}
	// moved advances mid-copy, so live progress isn't flat until a large file finishes.
	r.copyOpts.Progress = func(n int64) { r.moved.Add(n) }
	// Stops in-flight copies at the next chunk boundary a single time the run aborts.
	r.copyOpts.Cancel = func() bool { return r.abortErr() != nil }

	// Ctx cancellation (the first Ctrl-C) stops the walk. The workers still finish every queued file.
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if !opts.DryRun {
				r.note("interrupted: no new files; finishing the queue (Ctrl-C again to quit now)")
			}
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
		go func() { defer r.bgWg.Done(); r.runController(stopBg) }()
		go func() { defer r.bgWg.Done(); r.runWatchdog(stopBg) }()
		go func() { defer r.bgWg.Done(); r.runProgress(stopBg) }()
	}

	r.scanning.Store(true)
	err := r.walk(ctx)

	r.scanning.Store(false)
	r.jobs.close()
	if !opts.DryRun {
		r.wg.Wait()
	}
	close(stopWatch)

	r.applyHardlinks()
	if !opts.DryRun {
		r.metaJobs.close()
		r.metaWg.Wait()
	}
	// An interrupted walk saw only part of the source, so the passes that act on the whole tree are skipped.
	finished := ctx.Err() == nil && r.abortErr() == nil
	if opts.Mirror && finished {
		r.mirrorExtraneous()
	}
	if opts.Move && !opts.DryRun && finished {
		r.removeMovedSources(ctx)
	}
	if !opts.DryRun {
		close(stopBg)
		r.bgWg.Wait()
	}
	if err == nil {
		err = r.abortErr()
	}
	if err == nil && ctx.Err() != nil {
		err = ErrInterrupted
	}
	return r.summary(), err
}

// ErrInterrupted reports a run that stopped early on a cancel of its context.
var ErrInterrupted = errors.New("interrupted")

// halted reports whether the walk must stop: the run aborted, or the user interrupted it.
func (r *runner) halted(ctx context.Context) bool {
	return r.abortErr() != nil || ctx.Err() != nil
}

type fileJob struct {
	srcFS    vfs.FS
	src, dst string
	info     os.FileInfo
	parent   *dirState

	// checkUnchanged defers the skip check to the pool, so --checksum hashing runs in parallel.
	checkUnchanged bool

	// reason is set by the walk when checkUnchanged is false; otherwise the worker fills it in.
	reason string
}

type runner struct {
	opts     *options.Options
	copyOpts fsx.CopyOptions

	// reg owns remote connections; dfs/targetDir/targetFile are the destination path, "host:" prefix stripped.
	reg        *vfs.Registry
	dfs        vfs.FS
	targetDir  string
	targetFile string

	gate *gate
	jobs *queue[fileJob]
	wg   sync.WaitGroup // copy workers

	metaJobs *queue[*dirState]
	metaWg   sync.WaitGroup // directory metadata workers

	bgWg sync.WaitGroup // background goroutines: controller, watchdog, progress

	outMu     sync.Mutex
	stdout    io.Writer
	stderr    io.Writer
	progress  string // the live progress line on stderr, "" when none is drawn; guarded by outMu
	startedAt time.Time

	files, dirs, symlinks, bytes, skipped, failed, linked, deleted, updated atomic.Int64
	renamed, removed                                                        atomic.Int64

	// --move: source entries to remove a single time the run is done; movedDirs is walk-goroutine-owned.
	moveMu                    sync.Mutex
	movedFiles                []movedEntry
	movedDirs                 []movedEntry
	dirMetaTotal, dirMetaDone atomic.Int64

	// moved is a monotonic live-progress counter, distinct from bytes (the completed-copy total used for the summary).
	moved atomic.Int64

	// totalFiles/totalBytes grow as the walk discovers work, giving live progress and ETA without blocking copying.
	totalFiles, totalBytes atomic.Int64
	// scanning is true while the walk runs, so the totals can still grow.
	scanning atomic.Bool

	// freeBytes guards against ENOSPC; spaceCheck false means the guard is off.
	freeBytes  atomic.Int64
	spaceCheck bool

	abortMu sync.Mutex
	aborted error

	// Walk-goroutine-owned state (no locking needed).
	noFileIDsNoted bool // the "no inode identity over SSH" warning has been said
	onStack        map[string]bool
	hardlinkMap    map[string]hlPrimary // inode key -> primary destination
	hardlinks      []linkEnt            // secondary links to create after all copies
	mirrorRoots    []mirrorRoot         // dest/src pairs the --mirror pass may prune
	rootDev        uint64               // device of the current source root (a single
	rootDevSet     bool
}

// mirrorRoot pairs a source root with its destination; --mirror deletes only under recorded roots.
type mirrorRoot struct {
	srcFS     vfs.FS
	dest, src string
}

// resolveTarget splits the destination argument into the filesystem it lives on
// (dialing the host if it is remote) and the path on that filesystem. The rest
// of the engine never parses a "host:" prefix again.
func (r *runner) resolveTarget() error {
	arg := r.opts.TargetDir
	if arg == "" {
		arg = r.opts.TargetFile
	}
	dfs, path, err := r.reg.Resolve(arg)
	if err != nil {
		return err
	}
	r.dfs = dfs
	if r.opts.TargetDir != "" {
		r.targetDir = path
	} else {
		r.targetFile = path
	}
	return nil
}

// workerCount returns the gate ceiling and its initial limit.
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
	// Neutral start before the controller's earliest tick; it resizes from here.
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
	for {
		job, ok := r.jobs.pop()
		if !ok {
			return
		}
		if r.abortErr() != nil {
			r.completeDirWork(job.parent)
			continue // drain the queue without doing work
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
		v := scan.Compare(job.srcFS, job.src, job.info, r.dfs, job.dst, r.opts.Checksum)
		if !v.NeedCopy {
			// These bytes will never move: undo the discovery accounting.
			r.totalFiles.Add(-1)
			r.totalBytes.Add(-job.info.Size())
			r.finishUnchangedFile(job.srcFS, job.src, job.dst, job.info, v)
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
		n, err = fsx.CopyFile(job.srcFS, job.src, r.dfs, job.dst, job.info, r.copyOpts)
		if err == nil || isMetaErr(err) || attempt >= maxRetries || !retryable(err) || r.abortErr() != nil {
			break
		}
		time.Sleep(retryBackoff(attempt))
	}
	if isMetaErr(err) {
		r.warn("%v", err) // the data is published; only metadata is incomplete
		err = nil
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
	r.item("%s %s (%s)", r.transferVerb(), r.dfs.Display(job.dst), reason)
	r.moveSource(job.srcFS, job.src, job.dst)
}

// isMetaErr reports a copy whose data landed but whose metadata did not.
func isMetaErr(err error) bool {
	var me *fsx.MetaError
	return errors.As(err, &me)
}

// finishUnchangedFile finalizes a destination file whose content is already up
// to date: when metadata preservation is on and the verdict reports attribute
// drift. The attributes are synced (or, in a dry run, itemized) and the entry
// counts as updated. This also covers otherwise it is a plain skip. Attribute
// application is best-effort, matching the directory-metadata policy (a
// warning, not a failure).
func (r *runner) finishUnchangedFile(srcFS vfs.FS, srcPath, dstPath string, srcInfo os.FileInfo, v scan.Verdict) {
	defer r.moveUnchanged(srcFS, srcPath, dstPath)
	if r.copyOpts.Preserve {
		if diff := attrDiff(srcInfo, v); diff != "" {
			if !r.opts.DryRun {
				if err := fsx.SyncAttrs(srcFS, srcInfo, r.dfs, dstPath, v.ModeDiff, v.OwnerDiff, v.TimeDiff); err != nil {
					r.warn("metadata on %s: %v", r.dfs.Display(dstPath), err)
				}
			}
			r.updated.Add(1)
			r.item("%supdate %s (%s)", r.would(), r.dfs.Display(dstPath), diff)
			return
		}
	}
	r.skipped.Add(1)
	r.verbose("skip unchanged %s", r.dfs.Display(dstPath))
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
		// Every remaining file would hit ENOSPC too; abort now, don't grind through the tree.
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
		Renamed:  r.renamed.Load(),
		Removed:  r.removed.Load(),
		Bytes:    r.bytes.Load(),
		Cloned:   r.copyOpts.Dedup.Cloned(),
		Skipped:  r.skipped.Load(),
		Failed:   r.failed.Load(),
	}
}
