package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

func TestMirrorDeletesExtraneous(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "in.txt"), []byte("i"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	// Introduce extraneous content in the destination.
	writeFile(t, filepath.Join(dst, "src", "extra.txt"), []byte("x"), 0o644)
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "src", "extradir"), 0o755))
	writeFile(t, filepath.Join(dst, "src", "extradir", "junk"), []byte("j"), 0o644)

	om := &options.Options{Sources: []string{src}, TargetDir: dst, Mirror: true, Progress: "auto"}
	require.NoError(t, om.Validate())
	sum, err := Run(context.Background(), om)
	require.NoError(t, err)
	assert.NotZero(t, sum.Deleted)

	_, e1 := os.Stat(filepath.Join(dst, "src", "extra.txt"))
	assert.True(t, os.IsNotExist(e1), "extraneous file must be deleted")
	_, e2 := os.Stat(filepath.Join(dst, "src", "extradir"))
	assert.True(t, os.IsNotExist(e2), "extraneous dir must be deleted")
	_, e3 := os.Stat(filepath.Join(dst, "src", "keep.txt"))
	assert.NoError(t, e3, "matching file must be kept")
	_, e4 := os.Stat(filepath.Join(dst, "src", "sub", "in.txt"))
	assert.NoError(t, e4, "matching nested file must be kept")
}

// TestMirrorRestoresDirTimes: the mirror pass runs after the copy phase has
// already applied directory metadata, and deleting an extraneous entry bumps
// its parent's mtime — the pass must restore the preserved directory time.
func TestMirrorRestoresDirTimes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "d", "keep.txt"), []byte("k"), 0o644)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(src, "d"), old, old))

	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Mirror: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	// Plant an extraneous file and mirror again: deleting it bumps d's mtime
	// after d's metadata was applied.
	writeFile(t, filepath.Join(dst, "src", "d", "extra.txt"), []byte("x"), 0o644)
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Deleted)

	di, err := os.Lstat(filepath.Join(dst, "src", "d"))
	require.NoError(t, err)
	assert.WithinDuration(t, old, di.ModTime(), time.Second,
		"mirror deletions must not clobber preserved directory times")
}
