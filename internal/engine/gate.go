// gate is a resizable concurrency limiter; setLimit changes limit live, so a fixed pool need not resize.

package engine

import "sync"

// gate is a resizable concurrency limiter; setLimit changes limit live, so a fixed pool need not resize.
type gate struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int // current max concurrent holders (W)
	max   int // hard ceiling
	inUse int
	// waiting counts the workers that hold a job and have no slot.
	waiting int
}

func newGate(max, initial int) *gate {
	if max < 1 {
		max = 1
	}
	if initial < 1 {
		initial = 1
	}
	if initial > max {
		initial = max
	}
	g := &gate{limit: initial, max: max}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *gate) acquire() {
	g.mu.Lock()
	g.waiting++
	for g.inUse >= g.limit {
		g.cond.Wait()
	}
	g.waiting--
	g.inUse++
	g.mu.Unlock()
}

func (g *gate) release() {
	g.mu.Lock()
	g.inUse--
	g.cond.Signal()
	g.mu.Unlock()
}

// saturated reports whether every slot is in use; the connection scaler checks
// this before spending an SSH handshake.
func (g *gate) saturated() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inUse >= g.limit
}

// load reports the slots in use, the limit W, and the workers that wait for a slot.
func (g *gate) load() (inUse, limit, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inUse, g.limit, g.waiting
}

// current returns the live limit W.
func (g *gate) current() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

// The auto-scaling controller calls this every tick.
func (g *gate) setLimit(n int) {
	g.mu.Lock()
	if n < 1 {
		n = 1
	}
	if n > g.max {
		n = g.max
	}
	g.limit = n
	g.cond.Broadcast()
	g.mu.Unlock()
}
