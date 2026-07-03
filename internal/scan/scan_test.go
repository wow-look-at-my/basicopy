package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, path string, data []byte, mtime time.Time) os.FileInfo {
	t.Helper()
	require.NoError(t, os.WriteFile(path, data, 0o644))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	return fi
}

func TestCompareQuick(t *testing.T) {
	dir := t.TempDir()
	mt := time.Unix(1_700_000_000, 0)
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	si := write(t, src, []byte("hello"), mt)

	// Missing dst.
	assert.True(t, Compare(src, si, dst, false).NeedCopy)

	// Same size + mtime -> unchanged.
	write(t, dst, []byte("world"), mt) // same length, same mtime
	assert.False(t, Compare(src, si, dst, false).NeedCopy)

	// Different size.
	write(t, dst, []byte("longer"), mt)
	assert.True(t, Compare(src, si, dst, false).NeedCopy)

	// Same size, different mtime.
	write(t, dst, []byte("world"), mt.Add(time.Hour))
	assert.True(t, Compare(src, si, dst, false).NeedCopy)
}

func TestCompareChecksum(t *testing.T) {
	dir := t.TempDir()
	mt := time.Unix(1_700_000_000, 0)
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	si := write(t, src, []byte("identical"), mt)

	// Same content, but different mtime -> checksum still says unchanged.
	write(t, dst, []byte("identical"), mt.Add(time.Hour))
	assert.False(t, Compare(src, si, dst, true).NeedCopy)

	// Same size, different content -> changed.
	write(t, dst, []byte("identicaX"), mt)
	assert.True(t, Compare(src, si, dst, true).NeedCopy)
}

func TestCompareReasons(t *testing.T) {
	dir := t.TempDir()
	mt := time.Unix(1_700_000_000, 0)
	src := filepath.Join(dir, "src")
	si := write(t, src, []byte("hello"), mt)

	t.Run("missing dest is new", func(t *testing.T) {
		v := Compare(src, si, filepath.Join(dir, "missing"), false)
		assert.True(t, v.NeedCopy)
		assert.Equal(t, "new", v.Reason)
		assert.Nil(t, v.DstInfo)
	})

	t.Run("non-regular dest is a type change", func(t *testing.T) {
		d := filepath.Join(dir, "adir")
		require.NoError(t, os.Mkdir(d, 0o755))
		v := Compare(src, si, d, false)
		assert.True(t, v.NeedCopy)
		assert.Equal(t, "type change", v.Reason)
		assert.NotNil(t, v.DstInfo)
	})

	t.Run("size difference reports old and new", func(t *testing.T) {
		dst := filepath.Join(dir, "size")
		write(t, dst, []byte("hi"), mt)
		v := Compare(src, si, dst, false)
		assert.True(t, v.NeedCopy)
		assert.Equal(t, "size 2 -> 5", v.Reason)
	})

	t.Run("mtime difference in default mode", func(t *testing.T) {
		dst := filepath.Join(dir, "mtime")
		write(t, dst, []byte("world"), mt.Add(time.Hour))
		v := Compare(src, si, dst, false)
		assert.True(t, v.NeedCopy)
		assert.Equal(t, "mtime differs", v.Reason)
	})

	t.Run("content difference in checksum mode", func(t *testing.T) {
		dst := filepath.Join(dir, "content")
		write(t, dst, []byte("HELLO"), mt)
		v := Compare(src, si, dst, true)
		assert.True(t, v.NeedCopy)
		assert.Equal(t, "content differs", v.Reason)
	})

	t.Run("unchanged with mode drift", func(t *testing.T) {
		dst := filepath.Join(dir, "mode")
		write(t, dst, []byte("world"), mt) // default mode: size+mtime match
		require.NoError(t, os.Chmod(dst, 0o600))
		v := Compare(src, si, dst, false)
		assert.False(t, v.NeedCopy)
		assert.True(t, v.ModeDiff)
		assert.False(t, v.TimeDiff)
		require.NotNil(t, v.DstInfo, "the verdict must carry the dest info so callers don't re-stat")
		assert.Equal(t, os.FileMode(0o600), v.DstInfo.Mode().Perm())
	})

	t.Run("checksum-verified mtime drift wants a touchup", func(t *testing.T) {
		dst := filepath.Join(dir, "touch")
		write(t, dst, []byte("hello"), mt.Add(2*time.Hour))
		v := Compare(src, si, dst, true)
		assert.False(t, v.NeedCopy)
		assert.True(t, v.TimeDiff)
		assert.False(t, v.ModeDiff)
	})

	t.Run("mtime within tolerance needs no touchup", func(t *testing.T) {
		dst := filepath.Join(dir, "close")
		write(t, dst, []byte("hello"), mt.Add(500*time.Millisecond))
		v := Compare(src, si, dst, true)
		assert.False(t, v.NeedCopy)
		assert.False(t, v.TimeDiff)
	})
}

// BenchmarkHashFile measures the per-file cost of the --checksum content hash:
// read chunk size and allocation behavior both show up here.
func BenchmarkHashFile(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "f.bin")
	size := int64(8 << 20)
	require.NoError(b, os.WriteFile(path, make([]byte, size), 0o644))

	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		_, err := hashFile(path)
		require.Nil(b, err)

	}
}
