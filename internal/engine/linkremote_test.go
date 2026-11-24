package engine

import (
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// link is one network path that every connection shares: rate bytes per second in total.
type link struct {
	mu      sync.Mutex
	next    time.Time
	perByte time.Duration
}

// take blocks until the link has carried n more bytes. An idle link keeps a
// little credit, so a late wake-up of one sender does not lower the rate.
func (l *link) take(n int) {
	l.mu.Lock()
	now := time.Now()
	if credit := now.Add(-20 * time.Millisecond); l.next.Before(credit) {
		l.next = credit
	}
	l.next = l.next.Add(time.Duration(n) * l.perByte)
	wait := l.next.Sub(now)
	l.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

type linkWriter struct {
	io.WriteCloser
	l *link
}

func (w linkWriter) Write(p []byte) (int, error) {
	w.l.take(len(p))
	return w.WriteCloser.Write(p)
}

type linkReader struct {
	io.Reader
	l *link
}

func (r linkReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.l.take(n)
	return n, err
}

// linkRemote puts "pc:" behind one slow link. All connections share rate bytes
// per second, in both directions together. Each connection after the first takes dial to open.
func linkRemote(t *testing.T, rate int, dial time.Duration) {
	t.Helper()
	fakeRemote(t)
	inner := vfs.StartTransport
	l := &link{perByte: time.Second / time.Duration(rate)}
	vfs.StartTransport = func(c vfs.Connect) (*vfs.Transport, error) {
		if c.Extra {
			time.Sleep(dial)
		}
		tr, err := inner(c)
		if err != nil {
			return nil, err
		}
		tr.Stdin = linkWriter{WriteCloser: tr.Stdin, l: l}
		tr.Stdout = linkReader{Reader: tr.Stdout, l: l}
		return tr, nil
	}
}

// watchW records every W that the controller sets during a test.
func watchW(t *testing.T) func() []int {
	t.Helper()
	t.Serial("observeW is a package variable")
	var mu sync.Mutex
	var ws []int
	observeW = func(w int) {
		mu.Lock()
		ws = append(ws, w)
		mu.Unlock()
	}
	t.Cleanup(func() { observeW = nil })
	return func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), ws...)
	}
}

// TestLinkBoundRemoteKeepsWSmall is the regression test for a copy over SSH
// that went to the ceiling of W at once.
func TestLinkBoundRemoteKeepsWSmall(t *testing.T) {
	linkBoundCopy(t, false)
}

// TestLinkBoundDownloadKeepsWSmall is the same copy from the host to this machine. A read reports once per buffer too.
func TestLinkBoundDownloadKeepsWSmall(t *testing.T) {
	linkBoundCopy(t, true)
}

func linkBoundCopy(t *testing.T, download bool) {
	t.Helper()
	const (
		files = 40
		size  = 1 << 20
		rate  = 16 << 20
	)
	t.Serial("controlInterval is a package variable")
	restore := fastControlLoops(t)
	defer restore()
	// A slower tick than the other tests use: one tick must carry enough packets to measure a rate.
	controlInterval = 20 * time.Millisecond
	linkRemote(t, rate, 60*time.Millisecond)
	seen := watchW(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for i := 0; i < files; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("f%02d.bin", i)), make([]byte, size), 0o644)
	}

	o := &options.Options{
		Sources:    []string{src},
		TargetDir:  "pc:" + dst,
		MaxThreads: 256,
		Progress:   "never",
	}
	if download {
		o.Sources, o.TargetDir = []string{"pc:" + src}, dst
	}
	sum, _, _ := captureRun(t, o)
	require.EqualValues(t, files, sum.Files)
	require.Zero(t, sum.Failed)

	ws := seen()
	require.NotEmpty(t, ws)
	t.Logf("W went %v", compact(ws))
	assert.LessOrEqual(t, ws[0], 4, "a remote copy starts small")
	peak := 0
	for _, w := range ws {
		if w > peak {
			peak = w
		}
	}
	assert.LessOrEqual(t, peak, 16, "a link-bound copy must not pile on streams; W went %v", compact(ws))
}

// compact drops the repeats from a W history, for a failure message.
func compact(ws []int) []int {
	var out []int
	for _, w := range ws {
		if len(out) == 0 || out[len(out)-1] != w {
			out = append(out, w)
		}
	}
	return out
}

func TestUnsettledNamesEveryCaseThatDoesNotRunW(t *testing.T) {
	r := &runner{gate: newGate(8, 2), reg: vfs.NewRegistry()}
	assert.True(t, r.unsettled(), "no stream runs and no worker waits: the walk supplies too little work")

	r.gate.acquire()
	r.gate.acquire()
	assert.False(t, r.unsettled(), "W streams run")

	r.gate.setLimit(1)
	assert.True(t, r.unsettled(), "a trim does not stop a file that already started")

	r.gate.setLimit(3)
	assert.True(t, r.unsettled(), "a slot is free and no worker wants it")

	waiter := make(chan struct{})
	r.gate.setLimit(2)
	go func() {
		r.gate.acquire()
		close(waiter)
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := r.gate.load()
		return waiting == 1
	}, time.Second, time.Millisecond)
	assert.False(t, r.unsettled(), "W streams run and more work waits")

	r.gate.release()
	<-waiter
	r.gate.release()
	r.gate.release()
}

func TestWorkCountsFinishedFiles(t *testing.T) {
	r := &runner{}
	r.localMoved.Add(1000)
	assert.EqualValues(t, 1000, r.workDone())
	r.jobsDone.Add(3)
	assert.EqualValues(t, 1000+3*entryWork, r.workDone(), "a file that moves no data still counts as work")
}

// TestRemoteWorkIsCountedOnTheWire: a remote write reports once per buffer.
// The controller and the watchdog must see the bytes as they cross.
func TestRemoteWorkIsCountedOnTheWire(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "f.bin"), make([]byte, 200_000), 0o644)

	r := &runner{reg: vfs.NewRegistry()}
	defer r.reg.Close()
	fsys, _, err := r.reg.Resolve("pc:" + root)
	require.NoError(t, err)
	before, progress := r.workDone(), r.progressCount()

	f, err := fsys.Open(filepath.Join(root, "f.bin"))
	require.NoError(t, err)
	n, err := io.Copy(io.Discard, f)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	assert.GreaterOrEqual(t, r.workDone()-before, n, "the bytes of a remote read count as work, with no progress callback")
	assert.GreaterOrEqual(t, r.progressCount()-progress, n, "and as forward progress")
}
