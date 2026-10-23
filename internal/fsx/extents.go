package fsx

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/btree"
)

// Dedup remembers where shared source extents landed on the destination, so a
// later reference to the same extent is cloned there instead of copied again.
type Dedup struct {
	mu       sync.Mutex
	byDev    map[uint64]*extentIndex
	inflight []*claim
	canClone map[uint64]bool // per destination device, after the first probe
	cloned   atomic.Int64
}

// NewDedup returns an empty index.
func NewDedup() *Dedup {
	return &Dedup{byDev: map[uint64]*extentIndex{}, canClone: map[uint64]bool{}}
}

// Cloned reports the bytes that a range clone put on the destination without a copy.
func (d *Dedup) Cloned() int64 {
	if d == nil {
		return 0
	}
	return d.cloned.Load()
}

// destRef is a destination file that holds the bytes of recorded extents.
// While its copy runs, clones read the open temp file.
type destRef struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	dev, ino uint64
	dead     atomic.Bool // the file was removed or its bytes are not trusted
}

var errNoSource = errors.New("clone source is gone")

// with runs fn on an open handle to the file. The handle is valid only inside fn.
func (r *destRef) with(fn func(f *os.File) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead.Load() {
		return errNoSource
	}
	if r.f != nil {
		return fn(r.f)
	}
	if r.path == "" {
		return errNoSource
	}
	f, err := os.Open(r.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if !sameInode(f, r.dev, r.ino) {
		return errNoSource // the path no longer names the file basicopy wrote
	}
	return fn(f)
}

// extentLoc is one source extent and the destination range that holds its bytes.
type extentLoc struct {
	phys, length int64 // the physical range on the source device
	off          int64 // the logical offset of the bytes in ref
	ref          *destRef
}

// claim marks a source extent that a worker copies now. Others wait for it.
type claim struct {
	srcDev       uint64
	phys, length int64
	done         chan struct{}
}

// fileDedup is the dedup state of one CopyFile.
type fileDedup struct {
	srcDev uint64
	ref    *destRef
	recs   []extentLoc // the shared extents this file wrote
}

// detach stops clones from the temp file before CopyFile closes it.
func (f *fileDedup) detach() {
	if f.ref == nil {
		return
	}
	f.ref.mu.Lock()
	f.ref.f = nil
	f.ref.mu.Unlock()
}

// commit points the records of this file at its published path.
func (f *fileDedup) commit(path string) {
	if f.ref == nil {
		return
	}
	f.ref.mu.Lock()
	f.ref.path = path
	f.ref.mu.Unlock()
}

// kill makes every record of this file unusable.
func (f *fileDedup) kill() {
	if f.ref == nil {
		return
	}
	f.ref.mu.Lock()
	f.ref.dead.Store(true)
	f.ref.f = nil
	f.ref.mu.Unlock()
	f.ref, f.recs = nil, nil
}

// extentIndex holds the extents of one source device, ordered by phys, then by length.
type extentIndex struct {
	t *btree.BTreeG[extentLoc]
}

func newExtentIndex() *extentIndex {
	return &extentIndex{t: btree.NewG(32, func(a, b extentLoc) bool {
		if a.phys != b.phys {
			return a.phys < b.phys
		}
		return a.length < b.length
	})}
}

// findDepth bounds the backward scan. A miss only costs a normal copy.
const findDepth = 8

// find returns a live record that covers [phys, phys+length) and the offset of phys in it.
func (x *extentIndex) find(phys, length int64) (extentLoc, int64, bool) {
	var (
		hit   extentLoc
		found bool
		n     int
	)
	// Records can overlap, so a short record does not hide a longer one before it.
	x.t.DescendLessOrEqual(extentLoc{phys: phys, length: 1<<63 - 1}, func(r extentLoc) bool {
		if r.phys+r.length >= phys+length && !r.ref.dead.Load() {
			hit, found = r, true
			return false
		}
		n++
		return n < findDepth
	})
	if !found {
		return extentLoc{}, 0, false
	}
	return hit, phys - hit.phys, true
}

// acquire looks up a source extent. It returns a record that holds the bytes,
// or a claim that another worker holds (wait for it), or a new claim for the
// caller (mine: copy the bytes, then call release).
func (d *Dedup) acquire(srcDev uint64, phys, length int64) (loc extentLoc, delta int64, c *claim, mine bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if x := d.byDev[srcDev]; x != nil {
		if loc, delta, ok := x.find(phys, length); ok {
			return loc, delta, nil, false
		}
	}
	for _, c := range d.inflight {
		if c.srcDev == srcDev && c.phys <= phys && phys+length <= c.phys+c.length {
			return extentLoc{}, 0, c, false
		}
	}
	c = &claim{srcDev: srcDev, phys: phys, length: length, done: make(chan struct{})}
	d.inflight = append(d.inflight, c)
	return extentLoc{}, 0, c, true
}

// release ends a claim. A nil loc means the copy failed and nothing is recorded.
func (d *Dedup) release(c *claim, loc *extentLoc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if loc != nil {
		d.insertLocked(c.srcDev, *loc)
	}
	for i, x := range d.inflight {
		if x == c {
			d.inflight = append(d.inflight[:i], d.inflight[i+1:]...)
			break
		}
	}
	close(c.done)
}

func (d *Dedup) insert(srcDev uint64, loc extentLoc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.insertLocked(srcDev, loc)
}

func (d *Dedup) insertLocked(srcDev uint64, loc extentLoc) {
	x := d.byDev[srcDev]
	if x == nil {
		x = newExtentIndex()
		d.byDev[srcDev] = x
	}
	x.t.ReplaceOrInsert(loc)
}

// waitPoll is how often a wait for another worker's claim checks for cancel.
var waitPoll = 100 * time.Millisecond

// wait blocks until c ends. It reports false when the copy was canceled.
func wait(c *claim, o CopyOptions) bool {
	t := time.NewTicker(waitPoll)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return true
		case <-t.C:
			if o.canceled() {
				return false
			}
		}
	}
}

// cloneProbe returns the cached answer for dev, or asks probe once.
func (d *Dedup) cloneProbe(dev uint64, probe func() bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ok, known := d.canClone[dev]
	if !known {
		ok = probe()
		d.canClone[dev] = ok
	}
	return ok
}
