//go:build linux

package fsx

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// A cloud placeholder can report no allocated blocks and no data extents while
// read() still returns bytes. A pure hole is the same shape. The whole file
// reports zero blocks, so SEEK_DATA answers ENXIO, and a read returns the bytes
// the file holds. The copy must take its bytes from read(). The copy keep the
// hole a hole, rather than filling the destination with zeros the source never
// stored.
//
// Consider the mount-based version of this test. That version needed go-fuse,
// whose syscall constants this toolchain's Go fork does not have, so the file
// itself supplies the no-extent shape.
func TestCopyFileNoDataExtents(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cloud.bin")
	const size = 4 << 20

	f, err := os.Create(src)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())

	info, err := os.Lstat(src)
	require.NoError(t, err)
	require.True(t, isSparse(info), "the placeholder must take the sparse path")

	dst := filepath.Join(dir, "cloud.out")
	n, err := CopyFile(vfs.Local, src, vfs.Local, dst, info, CopyOptions{})
	require.NoError(t, err)
	assert.EqualValues(t, size, n)

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(make([]byte, size), got), "the copy must hold the source's bytes")

	di, err := os.Lstat(dst)
	require.NoError(t, err)
	st, ok := di.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	assert.Less(t, int64(st.Blocks)*512, int64(size), "the destination must keep the hole, not store the source's zeros")
}
