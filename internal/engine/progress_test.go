package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
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
		hardlinkMap: map[string]string{},
	}
	copyInfo, err := os.Stat(copyMe)
	require.NoError(t, err)
	skipInfo, err := os.Stat(skipMe)
	require.NoError(t, err)

	r.handleRegular(skipMe, filepath.Join(dst, "skip.txt"), skipInfo)
	r.handleRegular(copyMe, filepath.Join(dst, "copy.txt"), copyInfo)

	assert.EqualValues(t, 1, r.totalFiles.Load())
	assert.EqualValues(t, len("copy"), r.totalBytes.Load())
}
