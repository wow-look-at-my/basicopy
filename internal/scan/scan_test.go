package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wow-look-at-my/testify/assert"
	"github.com/wow-look-at-my/testify/require"
)

func write(t *testing.T, path string, data []byte, mtime time.Time) os.FileInfo {
	t.Helper()
	require.NoError(t, os.WriteFile(path, data, 0o644))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	return fi
}

func TestUnchangedQuick(t *testing.T) {
	dir := t.TempDir()
	mt := time.Unix(1_700_000_000, 0)
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	si := write(t, src, []byte("hello"), mt)

	// Missing dst.
	assert.False(t, Unchanged(src, si, dst, false))

	// Same size + mtime -> unchanged.
	write(t, dst, []byte("world"), mt) // same length, same mtime
	assert.True(t, Unchanged(src, si, dst, false))

	// Different size.
	write(t, dst, []byte("longer"), mt)
	assert.False(t, Unchanged(src, si, dst, false))

	// Same size, different mtime.
	write(t, dst, []byte("world"), mt.Add(time.Hour))
	assert.False(t, Unchanged(src, si, dst, false))
}

func TestUnchangedChecksum(t *testing.T) {
	dir := t.TempDir()
	mt := time.Unix(1_700_000_000, 0)
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	si := write(t, src, []byte("identical"), mt)

	// Same content, but different mtime -> checksum still says unchanged.
	write(t, dst, []byte("identical"), mt.Add(time.Hour))
	assert.True(t, Unchanged(src, si, dst, true))

	// Same size, different content -> changed.
	write(t, dst, []byte("identicaX"), mt)
	assert.False(t, Unchanged(src, si, dst, true))
}
