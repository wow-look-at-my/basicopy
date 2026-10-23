package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// cancelOnWrite cancels the run the first time a line that contains trigger is written.
type cancelOnWrite struct {
	mu      sync.Mutex
	trigger string
	cancel  context.CancelFunc
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.Contains(string(p), w.trigger) {
		w.cancel()
	}
	return len(p), nil
}

// TestInterruptDrainsQueue cancels the run in the middle of the walk. The files
// that were queued before the cancel must still be copied, the rest must not
// start. The run must report ErrInterrupted, not "context canceled". --mirror
// must not run on the partial walk.
func TestInterruptDrainsQueue(t *testing.T) {
	t.Serial()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	early := []string{"a1", "a2", "a3", "a4", "a5"}
	late := []string{"z1", "z2", "z3"}
	for _, n := range append(append([]string{}, early...), late...) {
		writeFile(t, filepath.Join(src, n), []byte(n), 0o644)
	}
	writeFile(t, filepath.Join(src, "m-cut"), []byte("x"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "extra"), []byte("keep me"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errOut bytes.Buffer
	oldOut, oldErr := runStdout, runStderr
	// The walk prints "exclude .../m-cut" after it queued the a* files and before it reaches the z* files.
	runStdout, runStderr = &cancelOnWrite{trigger: "exclude", cancel: cancel}, &errOut
	defer func() { runStdout, runStderr = oldOut, oldErr }()

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Exclude: []string{"m-cut"}, Mirror: true, Verbose: true, Progress: "never"}
	require.NoError(t, o.Validate())
	sum, err := Run(ctx, o)

	require.ErrorIs(t, err, ErrInterrupted)
	assert.False(t, errors.Is(err, context.Canceled), "the run error must not be a bare context cancel")
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, len(early), sum.Files, "every queued file finishes")
	for _, n := range early {
		assertSameContent(t, filepath.Join(src, n), filepath.Join(dst, "src", n))
	}
	for _, n := range late {
		_, statErr := os.Lstat(filepath.Join(dst, "src", n))
		assert.True(t, os.IsNotExist(statErr), "%s was not queued, so it must not be copied", n)
	}
	_, statErr := os.Lstat(filepath.Join(dst, "src", "extra"))
	assert.NoError(t, statErr, "--mirror must not delete on a partial walk")
	assert.Contains(t, errOut.String(), "interrupted: no new files")
	assert.NotContains(t, errOut.String(), "context canceled")
	assert.NotContains(t, errOut.String(), "error:")
}

// TestAbortSkipsHardlinks: after an abort the workers drop queued primaries, so a
// deferred hardlink to one must not run and fail with "no such file or directory".
func TestAbortSkipsHardlinks(t *testing.T) {
	root := t.TempDir()
	var errOut bytes.Buffer
	r := &runner{opts: &options.Options{}, stdout: &bytes.Buffer{}, stderr: &errOut, dfs: vfs.Local}
	r.hardlinks = []linkEnt{{target: filepath.Join(root, "never-copied"), dst: filepath.Join(root, "link"), srcFS: vfs.Local}}
	r.setAbort(errors.New("destination is out of space"))

	r.applyHardlinks()

	assert.Zero(t, r.failed.Load())
	assert.Zero(t, r.linked.Load())
	assert.Empty(t, errOut.String())
}
