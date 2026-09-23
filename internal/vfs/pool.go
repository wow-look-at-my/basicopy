// The SSH connection pool: how many connections a remote copy runs over, and
// which connection each operation uses. docs/ssh.md has the reasoning; briefly,
// an sftp-server answers a single request at a time, so transfers sharing a
// connection are transfers in a queue.
package vfs

import (
	"sync/atomic"

	"github.com/pkg/sftp"
)

// MaxConnections caps a host's pool; each connection is an ssh process, so the ceiling is not free.
const MaxConnections = 16

// conn is a single ssh process carrying a single SFTP session, plus the
// counters that say what it is doing.
type conn struct {
	client *sftp.Client
	close  func() error

	// transfers sizes the pool: another transfer waits rather than share a busy connection.
	transfers int64

	inflight atomic.Int64 // requests issued and not yet answered
}

// begin/finish bracket a single request on a connection.
func (c *conn) begin()  { c.inflight.Add(1) }
func (c *conn) finish() { c.inflight.Add(-1) }

// pick favors the free earliest connection, so directory listings never queue behind a slow transfer.
func (f *remoteFS) pick() *conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.conns) == 1 {
		return f.conns[0]
	}
	if f.conns[0].transfers == 0 {
		return f.conns[0]
	}
	return f.leastLoadedLocked()
}

// leastLoadedLocked returns the connection with the fewest requests outstanding,
// starting the scan at a rotating offset so equal loads spread out.
func (f *remoteFS) leastLoadedLocked() *conn {
	start := int(f.rr.Add(1)-1) % len(f.conns)
	best := f.conns[start]
	bestLoad := best.inflight.Load()
	for i := 1; i < len(f.conns) && bestLoad > 0; i++ {
		c := f.conns[(start+i)%len(f.conns)]
		if load := c.inflight.Load(); load < bestLoad {
			best, bestLoad = c, load
		}
	}
	return best
}

// run executes a single operation on a chosen connection, keeping that
// connection's in-flight count honest for the pool's decisions.
func run[T any](f *remoteFS, op func(*sftp.Client) (T, error)) (T, error) {
	c := f.pick()
	c.begin()
	v, err := op(c.client)
	c.finish()
	return v, err
}

func runErr(f *remoteFS, op func(*sftp.Client) error) error {
	_, err := run(f, func(c *sftp.Client) (struct{}, error) { return struct{}{}, op(c) })
	return err
}

// acquire reserves a connection, growing the pool as needed.
func (f *remoteFS) acquire() *conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		if c := f.freeConnLocked(); c != nil {
			c.transfers++
			return c
		}
		if !f.canGrowLocked() {
			c := f.leastLoadedLocked()
			c.transfers++
			return c
		}
		f.startDialLocked()
		f.free.Wait()
	}
}

// freeConnLocked returns a connection carrying no transfer, or nil if each of
// them is busy. A single time there is more than a single connection the
// earliest is reserved for metadata.
func (f *remoteFS) freeConnLocked() *conn {
	transferConns := f.conns
	if len(transferConns) > 1 {
		transferConns = transferConns[1:]
	}
	for _, c := range transferConns {
		if c.transfers == 0 {
			return c
		}
	}
	return nil
}

func (f *remoteFS) canGrowLocked() bool {
	return f.dial != nil && !f.frozen && !f.closed && len(f.conns) < MaxConnections
}

func (f *remoteFS) startDialLocked() {
	if f.dialing {
		return
	}
	f.dialing = true
	go func() {
		c, err := f.dial()

		f.mu.Lock()
		defer f.mu.Unlock()
		f.dialing = false
		if err != nil {
			// Usually the host wants an interactive credential; give up and share what is open.
			f.frozen = true
		} else {
			f.conns = append(f.conns, c)
		}
		f.free.Broadcast()
	}()
}

// release returns a transfer's connection to the pool.
func (f *remoteFS) release(c *conn) {
	f.mu.Lock()
	if c.transfers > 0 {
		c.transfers--
	}
	f.mu.Unlock()
	f.free.Signal()
}

// Pool reports how many connections are open and whether dialing more has been
// given up on.
func (f *remoteFS) Pool() (open int, frozen bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns), f.frozen
}
