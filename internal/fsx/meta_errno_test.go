package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// metaFailFS is the local FS with chmod, chown, and chtimes failing with err.
type metaFailFS struct {
	vfs.FS
	err error
}

func (f metaFailFS) Chmod(string, os.FileMode) error            { return f.err }
func (f metaFailFS) Chown(string, int, int) error               { return f.err }
func (f metaFailFS) Chtimes(string, time.Time, time.Time) error { return f.err }
func (f metaFailFS) Owner(os.FileInfo) (int, int, bool)         { return 1, 1, true }

func (f metaFailFS) CreateTemp(dir, pattern string) (vfs.File, error) {
	tf, err := f.FS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return chmodFailFile{tf, f.err}, nil
}

type chmodFailFile struct {
	vfs.File
	err error
}

func (c chmodFailFile) Chmod(os.FileMode) error { return c.err }

// TestMetadataFailureKeepsFileData: a failed chmod, chown, or chtimes is a
// metadata warning. The copied data is still published at the destination.
func TestMetadataFailureKeepsFileData(t *testing.T) {
	boom := errors.New("boom")
	fs := metaFailFS{vfs.Local, boom}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst")
	n, err := CopyFile(fs, src, fs, dst, info, CopyOptions{Preserve: true})
	var me *MetaError
	require.ErrorAs(t, err, &me)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "chmod")
	assert.Contains(t, err.Error(), "chown")
	assert.Contains(t, err.Error(), "set times")
	assert.Equal(t, int64(7), n)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))

	srcDir := filepath.Join(dir, "srcdir")
	dstDir := filepath.Join(dir, "dstdir")
	require.NoError(t, os.Mkdir(srcDir, 0o755))
	require.NoError(t, os.Mkdir(dstDir, 0o755))
	dinfo, err := os.Lstat(srcDir)
	require.NoError(t, err)
	assert.ErrorIs(t, ApplyMeta(fs, srcDir, fs, dstDir, dinfo, true), boom)
}

// TestUnsupportedMetadataIsSkipped: unsupported mode, owner, or times is silent.
func TestUnsupportedMetadataIsSkipped(t *testing.T) {
	assertMetaSkipped(t, &os.PathError{Op: "chmod", Path: "p", Err: errors.ErrUnsupported})
}

func assertMetaSkipped(t *testing.T, failure error) {
	t.Helper()
	fs := metaFailFS{vfs.Local, failure}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst")
	_, err = CopyFile(fs, src, fs, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))

	srcDir := filepath.Join(dir, "srcdir")
	dstDir := filepath.Join(dir, "dstdir")
	require.NoError(t, os.Mkdir(srcDir, 0o755))
	require.NoError(t, os.Mkdir(dstDir, 0o755))
	dinfo, err := os.Lstat(srcDir)
	require.NoError(t, err)
	require.NoError(t, ApplyMeta(fs, srcDir, fs, dstDir, dinfo, true))
}
