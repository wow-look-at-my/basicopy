//go:build linux

package fsx

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	var moved int64
	_, err = CopyFile(src, dst, info, CopyOptions{Preserve: true, Progress: func(d int64) { moved += d }})
	require.NoError(t, err)
	assert.Positive(t, moved, "sparse copy must report progress for its data regions")
	assert.LessOrEqual(t, moved, int64(size), "progress must not exceed the logical size")

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
	_, ok, _ := copyFileRange(dst, r1, 10, CopyOptions{})
	assert.False(t, ok, "copy_file_range from a pipe should report unsupported")

	r2, w2, err := os.Pipe()
	require.NoError(t, err)
	defer r2.Close()
	defer w2.Close()
	_, ok, _ = copySparse(dst, r2, 10, CopyOptions{BufSize: 4096})
	assert.False(t, ok, "SEEK_DATA on a pipe should report unsupported")
}

// TestCopyFileRangeCancelBetweenChunks shrinks the per-call cap so a small file
// takes several copy_file_range calls, then cancels after the first: the copy
// must stop at the next chunk boundary with ErrCanceled instead of finishing.
func TestCopyFileRangeCancelBetweenChunks(t *testing.T) {
	old := copyChunk
	copyChunk = 64 << 10
	defer func() { copyChunk = old }()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	const size = 256 << 10 // 4 chunks at the shrunken cap
	require.NoError(t, os.WriteFile(src, make([]byte, size), 0o644))
	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "dst.bin"))
	require.NoError(t, err)
	defer out.Close()

	canceled := false
	n, ok, err := copyFileRange(out, in, size, CopyOptions{
		Progress: func(int64) { canceled = true },
		Cancel:   func() bool { return canceled },
	})
	if !ok {
		t.Skip("copy_file_range unsupported on this filesystem")
	}
	require.ErrorIs(t, err, ErrCanceled)
	assert.EqualValues(t, 64<<10, n, "must stop after the chunk that flipped cancel")
}
