package engine

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/control"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// fakeRemoteFS is a stand-in for a remote host that records the widths asked of
// it. Only the pool methods are exercised; the filesystem methods are never
// called by the scaler.
type fakeRemoteFS struct {
	vfs.FS
	width int
	open  int
	calls []int
}

func newFakeRemoteFS() *fakeRemoteFS { return &fakeRemoteFS{width: 1, open: 1} }

func (f *fakeRemoteFS) Host() string { return "pc" }
func (f *fakeRemoteFS) SetWidth(n int) {
	f.calls = append(f.calls, n)
	f.width = n
	if n > f.open {
		f.open = n // dialing is instant here
	}
}
func (f *fakeRemoteFS) Pool() (int, int, bool) { return f.width, f.open, false }

// plateauedController returns a worker controller that has finished its initial
// climb, which is the precondition the connection scaler waits for.
func plateauedController(t *testing.T) *control.Controller {
	t.Helper()
	c := control.New(1, 8, 4)
	for i := 0; i < 40 && !c.Plateaued(); i++ {
		c.Update(control.Sample{Throughput: 1e6, SysCPU: -1, DevUtil: -1})
	}
	require.True(t, c.Plateaued(), "the worker controller must reach a plateau on a flat throughput curve")
	return c
}

// scalerRunner builds the minimal runner state the scaler reads: a gate it can
// ask about saturation and a byte counter it can differentiate.
func scalerRunner(t *testing.T, saturated bool) (*runner, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	r := &runner{
		opts:   &options.Options{Verbose: true},
		stdout: out,
		stderr: os.Stderr,
		gate:   newGate(4, 2),
		dfs:    vfs.Local,
	}
	if saturated {
		r.gate.acquire()
		r.gate.acquire()
	}
	return r, out
}

// feed runs one scaler tick that observes `moved` more bytes than the last.
func feed(s *connScaler, r *runner, c *control.Controller, remotes []vfs.FS, tick int, movedDelta int64) {
	r.moved.Add(movedDelta)
	s.tick(r, c, remotes, time.Unix(1_700_000_000, 0).Add(time.Duration(tick)*time.Second))
}

// TestConnScalerWaitsForSaturationAndPlateau: an SSH handshake is only spent
// once more parallel streams provably cannot help, so an unsaturated gate or a
// worker pool still climbing must leave the pool at one connection.
func TestConnScalerWaitsForSaturationAndPlateau(t *testing.T) {
	remote := newFakeRemoteFS()
	remotes := []vfs.FS{remote}

	// Idle worker slots: nothing to gain from another connection.
	r, _ := scalerRunner(t, false)
	s := newConnScaler()
	for i := 1; i <= 6; i++ {
		feed(s, r, plateauedController(t), remotes, i, 10<<20)
	}
	assert.Empty(t, remote.calls, "an unsaturated gate must never widen the pool")

	// Saturated, but the worker controller is still climbing.
	r, _ = scalerRunner(t, true)
	s = newConnScaler()
	climbing := control.New(1, 8, 4)
	require.False(t, climbing.Plateaued())
	for i := 1; i <= 6; i++ {
		feed(s, r, climbing, remotes, i, 10<<20)
	}
	assert.Empty(t, remote.calls, "during the worker climb the throughput signal belongs to the worker loop")
}

// TestConnScalerWidensWhenItPays: saturated, plateaued, and each added
// connection raises throughput -> the pool grows, bounded by MaxConnections.
func TestConnScalerWidensWhenItPays(t *testing.T) {
	remote := newFakeRemoteFS()
	remotes := []vfs.FS{remote}
	r, out := scalerRunner(t, true)
	workers := plateauedController(t)
	s := newConnScaler()

	rate := int64(10 << 20)
	for i := 1; i <= 60; i++ {
		feed(s, r, workers, remotes, i, rate)
		if remote.width > 1 {
			rate = int64(remote.width) * (10 << 20) // more connections really do help
		}
	}

	assert.Greater(t, remote.width, 1, "a rising plateau must buy more connections")
	assert.LessOrEqual(t, remote.open, vfs.MaxConnections, "growth stops at the cap")
	assert.Contains(t, out.String(), "ssh: pc now using 2 connections")
}

// TestConnScalerBacksOffWhenItDoesNot: the same conditions, but throughput is
// flat no matter how many connections are open -- the controller must return to
// the narrower pool instead of paying for connections that buy nothing.
func TestConnScalerBacksOffWhenItDoesNot(t *testing.T) {
	remote := newFakeRemoteFS()
	remotes := []vfs.FS{remote}
	r, _ := scalerRunner(t, true)
	workers := plateauedController(t)
	s := newConnScaler()

	for i := 1; i <= 40; i++ {
		feed(s, r, workers, remotes, i, 10<<20) // a hard ceiling: extra connections change nothing
	}

	require.NotEmpty(t, remote.calls)
	assert.Equal(t, 1, remote.width, "a connection that buys no throughput must stop carrying work")
}

// TestConnScalerIgnoresLocalRuns: with no remote host in the run there is
// nothing to scale, and the scaler must not touch the local FS.
func TestConnScalerIgnoresLocalRuns(t *testing.T) {
	r, out := scalerRunner(t, true)
	s := newConnScaler()
	for i := 1; i <= 10; i++ {
		feed(s, r, plateauedController(t), nil, i, 10<<20)
	}
	assert.Empty(t, out.String())
}
