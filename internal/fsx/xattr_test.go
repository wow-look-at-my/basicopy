//go:build darwin || linux || netbsd

package fsx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const testXattrName = "user.basicopy_test"

func setTestXattr(t *testing.T, path string, value []byte) {
	t.Helper()
	err := unix.Setxattr(path, testXattrName, value, 0)
	if ignorableXattrErr(err) {
		t.Skipf("xattrs unsupported on test filesystem: %v", err)
	}
	require.NoError(t, err)
}

func getTestXattr(t *testing.T, path string) []byte {
	t.Helper()
	size, err := unix.Getxattr(path, testXattrName, nil)
	require.NoError(t, err)
	buf := make([]byte, size)
	_, err = unix.Getxattr(path, testXattrName, buf)
	require.NoError(t, err)
	return buf
}

func TestCopyFilePreservesXattrs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
	setTestXattr(t, src, []byte("yes"))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst")
	_, err = CopyFile(src, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)
	assert.Equal(t, []byte("yes"), getTestXattr(t, dst))
}

func TestApplyMetaPreservesDirectoryXattrs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.Mkdir(src, 0o755))
	require.NoError(t, os.Mkdir(dst, 0o755))
	setTestXattr(t, src, []byte("dir"))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	require.NoError(t, ApplyMeta(src, dst, info, true))
	assert.Equal(t, []byte("dir"), getTestXattr(t, dst))
}
