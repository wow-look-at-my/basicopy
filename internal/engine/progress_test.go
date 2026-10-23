package engine

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

func TestProgressLineShowsLiveTotalsAndETA(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(2)
	r.totalFiles.Store(10)
	r.totalBytes.Store(1000)

	line := r.progressLine(250, 125, startedAt.Add(5*time.Second))

	assert.Contains(t, line, "2/10 files")
	assert.Contains(t, line, "250 B/1000 B (25.0%)")
	assert.Contains(t, line, "50 B/s avg")
	assert.Contains(t, line, "125 B/s current")
	assert.Contains(t, line, "ETA 15s")
}

func TestProgressLineMarksTotalsAsLowerBoundWhileScanning(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.scanning.Store(true)
	r.files.Store(2)
	r.totalFiles.Store(10)
	r.totalBytes.Store(1000)

	line := r.progressLine(250, 125, startedAt.Add(5*time.Second))

	assert.Contains(t, line, "2/10+ files")
	assert.Contains(t, line, "250 B/1000 B+ (25.0%)")
	assert.Contains(t, line, "ETA 15s+")
	assert.Contains(t, line, ", scanning")
}

func TestProgressLineWaitsForWalkBeforeMetadataTail(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.scanning.Store(true)
	r.files.Store(10)
	r.totalFiles.Store(10)
	r.totalBytes.Store(100)
	r.dirMetaDone.Store(3)
	r.dirMetaTotal.Store(8)

	line := r.progressLine(100, 0, startedAt.Add(time.Second))

	assert.NotContains(t, line, "metadata")
	assert.Contains(t, line, "scanning")
}

func TestProgressLineUsesFileTotalsForZeroByteWork(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(2)
	r.totalFiles.Store(4)

	line := r.progressLine(0, 0, startedAt.Add(time.Second))

	assert.Contains(t, line, "2/4 files")
	assert.Contains(t, line, "50.0%")
	assert.Contains(t, line, "0 B/s avg")
	assert.Contains(t, line, "0 B/s current")
	assert.Contains(t, line, "ETA 0s")
}

func TestProgressLineShowsMetadataTail(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(10)
	r.totalFiles.Store(10)
	r.totalBytes.Store(100)
	r.dirMetaDone.Store(3)
	r.dirMetaTotal.Store(8)

	line := r.progressLine(100, 0, startedAt.Add(time.Second))

	assert.Contains(t, line, "metadata 3/8 dirs")
	assert.Contains(t, line, "37.5%")
}

func TestMessageNeverJoinsProgressLine(t *testing.T) {
	var stderr bytes.Buffer
	r := &runner{opts: &options.Options{}, stdout: &stderr, stderr: &stderr}

	r.drawProgress("1/2 files")
	r.warn("skipping special file %s", "/dev/x")
	r.clearProgressLine()
	r.note("done")

	assert.Equal(t,
		"\r\033[K1/2 files"+
			"\r\033[Kbasicopy: warning: skipping special file /dev/x\n1/2 files"+
			"\r\033[K"+
			"basicopy: done\n",
		stderr.String(),
		"a message must erase the progress line, end with a newline, and redraw the progress line below it")
}

func TestStdoutItemNeverJoinsProgressLine(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &runner{opts: &options.Options{Verbose: true}, stdout: &stdout, stderr: &stderr}

	r.drawProgress("P")
	stderr.Reset()
	r.item("copy %s", "a.txt")

	assert.Equal(t, "copy a.txt\n", stdout.String())
	assert.Equal(t, "\r\033[KP", stderr.String(),
		"on a shared terminal, the progress line must be erased before a stdout line and drawn again after it")
}

func TestLineWriterSplitsChildOutputIntoLines(t *testing.T) {
	var out bytes.Buffer
	r := &runner{opts: &options.Options{}, stderr: &out}
	r.drawProgress("P")
	out.Reset()

	w := &lineWriter{r: r}
	_, err := w.Write([]byte("Warning: added host\r\nsecond "))
	require.NoError(t, err)
	_, err = w.Write([]byte("line\n"))
	require.NoError(t, err)

	assert.Equal(t, "\r\033[KWarning: added host\nP\r\033[Ksecond line\nP", out.String(),
		"each ssh line is printed whole above the progress line, and a partial line waits for its newline")
}

func TestDiscoveryTotalsCountQueuedCopyWork(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	copyMe := filepath.Join(src, "copy.txt")
	skipMe := filepath.Join(src, "skip.txt")
	writeFile(t, copyMe, []byte("copy"), 0o644)
	writeFile(t, skipMe, []byte("skip"), 0o644)
	writeFile(t, filepath.Join(dst, "skip.txt"), []byte("skip"), 0o644)
	srcInfo, err := os.Stat(skipMe)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(dst, "skip.txt"), srcInfo.ModTime(), srcInfo.ModTime()))

	r := &runner{
		opts:        &options.Options{DryRun: true, Progress: "auto"},
		hardlinkMap: map[string]hlPrimary{},
		stdout:      io.Discard, // dry runs itemize by default
		dfs:         vfs.Local,
	}
	sroot := &srcRoot{fs: vfs.Local}
	copyInfo, err := os.Stat(copyMe)
	require.NoError(t, err)
	skipInfo, err := os.Stat(skipMe)
	require.NoError(t, err)

	r.handleRegular(sroot, skipMe, filepath.Join(dst, "skip.txt"), skipInfo, nil)
	r.handleRegular(sroot, copyMe, filepath.Join(dst, "copy.txt"), copyInfo, nil)

	assert.EqualValues(t, 1, r.totalFiles.Load())
	assert.EqualValues(t, len("copy"), r.totalBytes.Load())
}
