package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

func TestProgressFrameShowsLiveTotalsAndETA(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(2)
	r.skipped.Store(3)
	r.totalFiles.Store(10)
	r.totalBytes.Store(1000)
	r.moved.Store(250)

	f := frame(t, r, startedAt.Add(5*time.Second))

	assert.Contains(t, f, "basicopy: 2/10 files, 3 skipped")
	assert.Contains(t, f, "avg 50 B/s")
	assert.Regexp(t, `TOTAL .* 25%\s+250 B / 1000 B .* ETA 15s`, f)
	assert.NotContains(t, f, "scanning")
}

func TestProgressFrameMarksTotalsAsLowerBoundWhileScanning(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.scanning.Store(true)
	r.files.Store(2)
	r.totalFiles.Store(10)
	r.totalBytes.Store(1000)
	r.moved.Store(250)

	f := frame(t, r, startedAt.Add(5*time.Second))

	assert.Contains(t, f, "2/10+ files")
	assert.Contains(t, f, "250 B / 1000 B+")
	assert.Contains(t, f, "ETA 15s+")
	assert.Contains(t, f, "scanning the source")
}

func TestProgressFrameWaitsForWalkBeforeMetadataTail(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.scanning.Store(true)
	r.files.Store(10)
	r.totalFiles.Store(10)
	r.totalBytes.Store(100)
	r.moved.Store(100)
	r.dirMetaDone.Store(3)
	r.dirMetaTotal.Store(8)

	f := frame(t, r, startedAt.Add(time.Second))

	assert.NotContains(t, f, "metadata")
	assert.Contains(t, f, "scanning")
}

func TestProgressFrameUsesFileTotalsForZeroByteWork(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(2)
	r.totalFiles.Store(4)

	f := frame(t, r, startedAt.Add(time.Second))

	assert.Contains(t, f, "2/4 files")
	assert.Regexp(t, `TOTAL .* 50%`, f)
}

func TestProgressFrameShowsSkippedWithoutTotals(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(1)
	r.skipped.Store(4)

	f := frame(t, r, startedAt.Add(time.Second))

	assert.Contains(t, f, "1/0 files, 4 skipped")
	assert.Contains(t, f, "0 B / ?", "no byte total yet is a question mark, not a zero")
}

func TestProgressFrameShowsMetadataTail(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.files.Store(10)
	r.totalFiles.Store(10)
	r.totalBytes.Store(100)
	r.moved.Store(100)
	r.dirMetaDone.Store(3)
	r.dirMetaTotal.Store(8)

	f := frame(t, r, startedAt.Add(time.Second))

	assert.Contains(t, f, "metadata 3/8 dirs (37.5%)")
}

func TestProgressFrameShowsEachFileInFlight(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	r.totalFiles.Store(2)
	r.totalBytes.Store(3000)
	big := r.active.add("photos/big.raw", 2000, startedAt)
	big.done.Store(500)
	r.active.add("empty.txt", 0, startedAt)

	f := frame(t, r, startedAt.Add(5*time.Second))

	assert.Regexp(t, `photos/big\.raw .* 25%\s+500 B / 2.0 KiB\s+100 B/s\s+ETA 15s`, f)
	assert.Contains(t, f, "empty.txt")
	assert.Less(t, strings.Index(f, "big.raw"), strings.Index(f, "empty.txt"), "the oldest file keeps the top row")
	assert.Less(t, strings.Index(f, "empty.txt"), strings.Index(f, "TOTAL"), "TOTAL is the last row")
}

func TestProgressFrameFoldsExtraFilesIntoOneRow(t *testing.T) {
	startedAt := time.Unix(100, 0)
	r := &runner{startedAt: startedAt}
	for i := 0; i < maxTransferRows+3; i++ {
		r.active.add(fmt.Sprintf("file-%02d", i), 10, startedAt)
	}

	f := frame(t, r, startedAt.Add(time.Second))

	assert.Contains(t, f, fmt.Sprintf("file-%02d", maxTransferRows-1))
	assert.NotContains(t, f, fmt.Sprintf("file-%02d", maxTransferRows))
	assert.Contains(t, f, "+3 more")
}

func TestProgressFrameSaysWhenInterrupted(t *testing.T) {
	r := &runner{startedAt: time.Unix(100, 0)}
	r.scanning.Store(true)
	r.draining.Store(true)

	f := frame(t, r, time.Unix(101, 0))

	assert.Contains(t, f, "interrupted: finishing the queue")
	assert.NotContains(t, f, "scanning the source", "the interrupt names the phase, not the stopped walk")
}

func TestLineWriterSplitsChildOutputIntoLines(t *testing.T) {
	var out bytes.Buffer
	r := &runner{opts: &options.Options{}, stderr: &out}

	w := &lineWriter{r: r}
	_, err := w.Write([]byte("Warning: added host\r\nsecond "))
	require.NoError(t, err)
	assert.Equal(t, "Warning: added host\n", out.String(), "a partial line waits for its newline")
	_, err = w.Write([]byte("line\n"))
	require.NoError(t, err)

	assert.Equal(t, "Warning: added host\nsecond line\n", out.String())
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
