package engine

import (
	"bytes"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;:?]*[A-Za-z]`)

// frame renders one frame of the region at a width wide enough for every column, without colour.
func frame(t *testing.T, r *runner, now time.Time) string {
	t.Helper()
	view, err := progressView()
	require.NoError(t, err)
	out, err := view.Render(r.progressProps(now), 120, 14)
	require.NoError(t, err)
	return sgr.ReplaceAllString(out, "")
}

// syncBuffer is a terminal that the region loop and the test both write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startRegion runs the region the way Run does, and returns the call that stops it.
func startRegion(t *testing.T, r *runner) (stop func()) {
	t.Helper()
	ch := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.runProgress(ch)
	}()
	require.Eventually(t, func() bool {
		r.outMu.Lock()
		defer r.outMu.Unlock()
		return r.display != nil
	}, 5*time.Second, 5*time.Millisecond, "the region never came up")
	return func() {
		close(ch)
		<-done
	}
}

func TestMessagesPrintAboveTheRegion(t *testing.T) {
	t.Serial("a region registers with the process-wide inspector")
	stderr := &syncBuffer{}
	var stdout bytes.Buffer
	r := &runner{opts: &options.Options{Progress: "always", Verbose: true}, stdout: &stdout, stderr: stderr, startedAt: time.Now()}
	r.totalFiles.Store(1)
	stop := startRegion(t, r)
	require.Eventually(t, func() bool { return bytes.Contains([]byte(stderr.String()), []byte("TOTAL")) },
		5*time.Second, 5*time.Millisecond, "the region never painted")

	r.warn("skipping special file %s", "/dev/x")
	r.item("copy %s", "a.txt")
	stop()
	r.note("done")

	out := sgr.ReplaceAllString(stderr.String(), "")
	assert.Contains(t, out, "\rbasicopy: warning: skipping special file /dev/x\r\n", "the message takes a whole row above the region")
	assert.Equal(t, "copy a.txt\n", stdout.String(), "a stdout that is not the terminal gets its line straight")
	assert.Contains(t, out, "basicopy: done\n", "after the region goes, a line writes straight through")
	assert.Nil(t, r.display)
}

func TestProgressNeverDrawsWithoutATerminal(t *testing.T) {
	var stderr bytes.Buffer
	r := &runner{opts: &options.Options{Progress: "auto"}, stderr: &stderr}
	assert.False(t, r.progressEnabled())
	r.runProgress(make(chan struct{}))
	assert.Empty(t, stderr.String())
}
