//go:build linux

package fsx

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/wow-look-at-my/testify/assert"
	"github.com/wow-look-at-my/testify/require"
)

// TestCopyFileSparse verifies that a sparse source is copied with identical
// content and that the destination remains sparse (holes preserved).
func TestCopyFileSparse(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sparse.bin")
	const size = 8 << 20 // 8 MiB logical, data only at the ends

	f, err := os.Create(src)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte("HEAD"), 0)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte("TAIL"), size-4)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())

	info, err := os.Lstat(src)
	require.NoError(t, err)
	dst := filepath.Join(dir, "sparse.out")
	_, err = CopyFile(src, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)

	sd, err := os.ReadFile(src)
	require.NoError(t, err)
	dd, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, sd, dd, "content must be byte-identical")

	di, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.EqualValues(t, size, di.Size())

	st, ok := di.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	assert.Less(t, int64(st.Blocks)*512, int64(size), "destination should be sparse (holes preserved)")
}

// A pipe supports neither copy_file_range nor SEEK_DATA, so both helpers must
// report "unsupported" (ok=false) so the caller falls back to a buffered copy.
func TestFastPathsFallBackOnPipe(t *testing.T) {
	dir := t.TempDir()
	dst, err := os.Create(filepath.Join(dir, "d"))
	require.NoError(t, err)
	defer dst.Close()

	r1, w1, err := os.Pipe()
	require.NoError(t, err)
	defer r1.Close()
	defer w1.Close()
	_, ok, _ := copyFileRange(dst, r1, 10)
	assert.False(t, ok, "copy_file_range from a pipe should report unsupported")

	r2, w2, err := os.Pipe()
	require.NoError(t, err)
	defer r2.Close()
	defer w2.Close()
	_, ok, _ = copySparse(dst, r2, 10, 4096)
	assert.False(t, ok, "SEEK_DATA on a pipe should report unsupported")
}
