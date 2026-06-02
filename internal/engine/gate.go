package engine

import "sync"

// gate is a resizable concurrency limiter. At most `limit` holders may hold the
// gate concurrently; the limit can be changed at runtime (the auto-scaling
// controller uses setLimit to grow and shrink active parallelism). A fixed pool
// of worker goroutines acquires the gate before each job, so changing the limit
// changes how many of them may run at once without churning goroutines.
type gate struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int // current max concurrent holders (W)
	max   int // hard ceiling
	inUse int
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
	for g.inUse >= g.limit {
		g.cond.Wait()
	}
	g.inUse++
	g.mu.Unlock()
}

func (g *gate) release() {
	g.mu.Lock()
	g.inUse--
	g.cond.Signal()
	g.mu.Unlock()
}

// setLimit changes the number of concurrent holders allowed, clamped to [1,max].
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
