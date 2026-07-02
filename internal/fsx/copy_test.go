package fsx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyFileRegular(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	data := make([]byte, 300_000)
	for i := range data {
		data[i] = byte(i*131 + 7)
	}
	require.NoError(t, os.WriteFile(src, data, 0o640))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst.bin")
	n, err := CopyFile(src, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)
	assert.EqualValues(t, len(data), n)

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	di, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), di.Mode().Perm())

	// No leftover temp file should remain in the directory.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".basicopy-tmp-"), "temp file leaked: %s", e.Name())
	}
}

func TestCopyFileEmpty(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(src, nil, 0o644))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "empty.out")
	n, err := CopyFile(src, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)
	assert.Zero(t, n)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCopyFileErrors(t *testing.T) {
	dir := t.TempDir()

	// Missing source -> open fails.
	_, err := CopyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "out"), nil, CopyOptions{})
	assert.Error(t, err)

	// Destination directory does not exist -> temp creation fails.
	src := filepath.Join(dir, "s")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o644))
	info, err := os.Lstat(src)
	require.NoError(t, err)
	_, err = CopyFile(src, filepath.Join(dir, "missing-dir", "out"), info, CopyOptions{})
	assert.Error(t, err)
}

func TestPlainCopyDirect(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	want := []byte("0123456789abcdef")
	require.NoError(t, os.WriteFile(src, want, 0o644))

	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "b"))
	require.NoError(t, err)

	n, err := plainCopy(out, in, 4, nil) // tiny buffer forces the copy loop
	require.NoError(t, err)
	assert.EqualValues(t, len(want), n)
	require.NoError(t, out.Close())

	got, err := os.ReadFile(filepath.Join(dir, "b"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestPlainCopyProgress checks that the buffered path reports incremental
// progress: a file larger than the copy's internal chunk fires the callback more
// than once, and the reported bytes sum to the file size.
func TestPlainCopyProgress(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	want := make([]byte, 1<<20) // 1 MiB -> many reads through the copy loop
	for i := range want {
		want[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(src, want, 0o644))

	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "b"))
	require.NoError(t, err)
	defer out.Close()

	var total int64
	var calls int
	n, err := plainCopy(out, in, 64<<10, func(d int64) { total += d; calls++ })
	require.NoError(t, err)
	assert.EqualValues(t, len(want), n)
	assert.EqualValues(t, len(want), total, "progress must sum to bytes copied")
	assert.Greater(t, calls, 1, "a multi-chunk copy must report progress incrementally")
}

// TestCopyFileReportsProgress checks that CopyFile threads its Progress callback
// through whichever copy path runs, and that the callback accounts for the whole
// file.
func TestCopyFileReportsProgress(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	data := make([]byte, 256*1024) // larger than the default read chunk
	for i := range data {
		data[i] = byte(i*31 + 5)
	}
	require.NoError(t, os.WriteFile(src, data, 0o644))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	var total int64
	dst := filepath.Join(dir, "dst.bin")
	n, err := CopyFile(src, dst, info, CopyOptions{Progress: func(d int64) { total += d }})
	require.NoError(t, err)
	assert.EqualValues(t, len(data), n)
	assert.EqualValues(t, n, total, "progress callback must account for every copied byte")
}

// TestMetaUnsupported checks the predicate that lets metadata preservation skip
// operations a destination filesystem cannot perform (FAT/exFAT/NTFS), so a copy
// whose data succeeded is never discarded over a chmod/chown/utimes failure.
func TestMetaUnsupported(t *testing.T) {
	assert.True(t, metaUnsupported(errors.ErrUnsupported))
	// Real callers wrap the syscall error (e.g. with the path); errors.Is must
	// still see through the wrapping.
	assert.True(t, metaUnsupported(fmt.Errorf("chmod /mnt/x: %w", errors.ErrUnsupported)))
	assert.False(t, metaUnsupported(nil))
	assert.False(t, metaUnsupported(errors.New("some other failure")))
	assert.False(t, metaUnsupported(os.ErrPermission))
}

func TestCopyFileFsync(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("durable"), 0o644))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst")
	_, err = CopyFile(src, dst, info, CopyOptions{Preserve: true, Fsync: true})
	require.NoError(t, err)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, []byte("durable"), got)
}

// BenchmarkPlainCopyProgress measures the buffered fallback path exactly as the
// engine drives it (a live progress callback is always installed): chunk size
// and per-file allocation behavior both show up here.
func BenchmarkPlainCopyProgress(b *testing.B) {
	dir := b.TempDir()
	src := filepath.Join(dir, "src.bin")
	size := int64(8 << 20)
	require.NoError(b, os.WriteFile(src, make([]byte, size), 0o644))

	in, err := os.Open(src)
	require.NoError(b, err)
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "dst.bin"))
	require.NoError(b, err)
	defer out.Close()

	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		_, err := in.Seek(0, io.SeekStart)
		require.NoError(b, err)
		_, err = out.Seek(0, io.SeekStart)
		require.NoError(b, err)
		n, err := plainCopy(out, in, 0, func(int64) {})
		require.NoError(b, err)
		require.EqualValues(b, size, n)
	}
}
