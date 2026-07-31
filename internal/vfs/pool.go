// The SSH connection pool: how many connections a remote copy runs over, and
// which connection each operation uses. docs/ssh.md has the reasoning; briefly,
// an sftp-server answers one request at a time, so transfers sharing a
// connection are transfers in a queue.
package vfs

import (
	"sync/atomic"

	"github.com/pkg/sftp"
)

// MaxConnections caps a host's connection pool. Each connection is an ssh
// process on both ends, so the ceiling is not free -- but it has to be high
// enough for the case that needs connections: an sftp-server answers one
// request at a time, so concurrent transfers sharing a connection are run one
// after another by the far side, however many of them the copy has in flight.
const MaxConnections = 16

// conn is one ssh process carrying one SFTP session, plus the counters that say
// what it is doing.
type conn struct {
	client *sftp.Client
	close  func() error

	// transfers is the number of open file handles this connection is copying
	// through. It is what the pool sizes itself by: a second transfer on a
	// connection already carrying one waits for the first, because the server
	// works through its requests in order.
	transfers int64

	inflight atomic.Int64 // requests issued and not yet answered
}

// begin/finish bracket one request on a connection.
func (c *conn) begin()  { c.inflight.Add(1) }
func (c *conn) finish() { c.inflight.Add(-1) }

// pick chooses the connection for a one-off operation -- a stat, a readdir, a
// rename. Once the pool has grown, the first connection is kept clear of file
// transfers (see acquire) and is preferred here, so the walk's directory
// listings never end up queued behind a file that takes a minute to read. With
// a single connection there is no choice to make.
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

// run executes one operation on a chosen connection, keeping that connection's
// in-flight count honest for the pool's decisions.
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

// acquire reserves a connection for one file transfer, and is where the pool
// grows. A transfer takes a connection that is carrying none; if there is no
// such connection, the pool dials one and this transfer waits for it -- or for
// an earlier transfer to finish, whichever comes first.
//
// Waiting is the point. Once a file handle is opened on a connection it cannot
// be moved, and the far side runs that connection's requests in order, so
// putting a second transfer on a busy connection commits it to going second.
// A copy that does this for its first burst of files ends up transferring them
// one after another no matter how wide the pool grows afterwards.
//
// At MaxConnections, or when dialing has failed, transfers share connections
// rather than wait forever.
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

// freeConnLocked returns a connection carrying no transfer, or nil if every one
// of them is busy. Once there is more than one connection the first is reserved
// for metadata.
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

// startDialLocked opens one more connection in the background, one at a time so
// a run never storms a host with simultaneous handshakes.
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
			// The usual cause is a host that wants an interactive credential,
			// which extra connections deliberately refuse to ask for. Stop
			// trying and let the waiters share what is already open.
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
