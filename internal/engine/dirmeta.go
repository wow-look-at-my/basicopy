package engine

import (
	"os"
	"runtime"
	"sync/atomic"

	"github.com/wow-look-at-my/basicopy/internal/fsx"
)

type dirState struct {
	src, dst string
	info     os.FileInfo
	pending  atomic.Int64
	closed   atomic.Bool
	queued   atomic.Bool
}

func metadataWorkerCount() int {
	n := runtime.NumCPU()
	if n < 4 {
		return 4
	}
	if n > 32 {
		return 32
	}
	return n
}

func (r *runner) metadataWorker() {
	defer r.metaWg.Done()
	for d := range r.metaJobs {
		if err := fsx.ApplyMeta(d.src, d.dst, d.info, r.copyOpts.Preserve); err != nil {
			r.warn("metadata on %s: %v", d.dst, err)
		}
		r.dirMetaDone.Add(1)
	}
}

func (r *runner) newDirState(src, dst string, info os.FileInfo) *dirState {
	if r.opts.DryRun || !r.copyOpts.Preserve {
		return nil
	}
	r.dirMetaTotal.Add(1)
	return &dirState{src: src, dst: dst, info: info}
}

func (r *runner) addDirWork(d *dirState) {
	if d != nil {
		d.pending.Add(1)
	}
}

func (r *runner) completeDirWork(d *dirState) {
	if d == nil {
		return
	}
	if d.pending.Add(-1) == 0 && d.closed.Load() {
		r.queueDirMeta(d)
	}
}

func (r *runner) closeDir(d *dirState) {
	if d == nil {
		return
	}
	d.closed.Store(true)
	if d.pending.Load() == 0 {
		r.queueDirMeta(d)
	}
}

// queueDirMeta applies directory metadata as soon as this specific directory can
// no longer have child entries created by regular copies or deferred hardlinks.
func (r *runner) queueDirMeta(d *dirState) {
	if d.queued.CompareAndSwap(false, true) {
		r.metaJobs <- d
	}
}
