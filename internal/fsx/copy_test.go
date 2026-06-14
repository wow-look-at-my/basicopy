package fsx

import (
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

	n, err := plainCopy(out, in, 4) // tiny buffer forces the copy loop
	require.NoError(t, err)
	assert.EqualValues(t, len(want), n)
	require.NoError(t, out.Close())

	got, err := os.ReadFile(filepath.Join(dir, "b"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
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
