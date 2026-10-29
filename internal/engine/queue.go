// queue is an unbounded FIFO.

package engine

import "sync"

// queue is an unbounded FIFO.
type queue[T any] struct {
	mu     sync.Mutex
	cond   sync.Cond
	items  []T
	closed bool
}

func newQueue[T any]() *queue[T] {
	q := &queue[T]{}
	q.cond.L = &q.mu
	return q
}

func (q *queue[T]) push(v T) {
	q.mu.Lock()
	q.items = append(q.items, v)
	q.mu.Unlock()
	q.cond.Signal()
}

// close makes pop return false once the queue is empty.
func (q *queue[T]) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop blocks until an item is available. It returns false when the queue is
// closed and empty.
func (q *queue[T]) pop() (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	var zero T
	if len(q.items) == 0 {
		return zero, false
	}
	v := q.items[0]
	q.items[0] = zero // release the reference; append drops the dead prefix on growth
	q.items = q.items[1:]
	return v, true
}
