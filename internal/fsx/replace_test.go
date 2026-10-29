package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// ntRenameFS refuses a rename over a file with no owner write bit, as NT does for a read-only file.
type ntRenameFS struct {
	vfs.FS
	renameErr error
}

func (f ntRenameFS) Rename(from, to string) error {
	if fi, err := os.Lstat(to); err == nil && fi.Mode().Perm()&0o200 == 0 {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
	}
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.FS.Rename(from, to)
}

func TestCopyFileReplacesReadOnlyTarget(t *testing.T) {
	fs := ntRenameFS{FS: vfs.Local}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o444))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o444))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	_, err = CopyFile(fs, src, fs, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
	fi, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o444), fi.Mode().Perm(), "the copy keeps the source's read-only mode")
}

func TestReplaceRestoresModeWhenRetryFails(t *testing.T) {
	boom := errors.New("boom")
	fs := ntRenameFS{FS: vfs.Local, renameErr: boom}
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(tmp, []byte("new"), 0o644))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o444))

	assert.ErrorIs(t, replace(fs, tmp, dst), boom)
	fi, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o444), fi.Mode().Perm())
}

func TestReplaceLeavesOtherRefusalsAlone(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp")
	require.NoError(t, os.WriteFile(tmp, []byte("new"), 0o644))
	// A missing target is refused for another reason, so there is nothing to clear.
	err := replace(ntRenameFS{FS: vfs.Local, renameErr: os.ErrPermission}, tmp, filepath.Join(dir, "missing"))
	assert.ErrorIs(t, err, os.ErrPermission)
}
