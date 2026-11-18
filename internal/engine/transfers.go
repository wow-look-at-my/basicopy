// transfers.go holds the files a worker copies now, for the progress display.

package engine

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// transfer is one file in flight.
type transfer struct {
	seq   uint64
	label string
	size  int64
	start time.Time
	done  atomic.Int64
}

// transfers is the set of files in flight. Workers add and remove; the display reads.
type transfers struct {
	mu   sync.Mutex
	next uint64
	live map[uint64]*transfer
}

func (ts *transfers) add(label string, size int64, now time.Time) *transfer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.live == nil {
		ts.live = make(map[uint64]*transfer)
	}
	ts.next++
	t := &transfer{seq: ts.next, label: label, size: size, start: now}
	ts.live[t.seq] = t
	return t
}

func (ts *transfers) remove(t *transfer) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.live, t.seq)
}

// snapshot lists the files in flight, oldest first, so a row keeps its place from one frame to the next.
func (ts *transfers) snapshot() []*transfer {
	ts.mu.Lock()
	out := make([]*transfer, 0, len(ts.live))
	for _, t := range ts.live {
		out = append(out, t)
	}
	ts.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// transferLabel names a file by its path under the target, which is what.
func (r *runner) transferLabel(job fileJob) string {
	if r.targetDir != "" {
		if rel, err := r.dfs.Rel(r.targetDir, job.dst); err == nil && rel != "" && rel != "." {
			return rel
		}
	}
	return r.dfs.Base(job.dst)
}
