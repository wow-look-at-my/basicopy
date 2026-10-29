//go:build darwin || linux || netbsd || cosmo

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
	"golang.org/x/sys/unix"
)

// failXattr swaps the xattr syscall seam so op ("list", "get", "set") fails with
// errno. The other ops report a single attribute "user.x" with value "v".
func failXattr(t *testing.T, op string, errno error) {
	t.Helper()
	old := xattrSys
	t.Cleanup(func() { xattrSys = old })
	name := []byte("user.x\x00")
	xattrSys.list = func(_ string, dest []byte, _ bool) (int, error) {
		if op == "list" {
			return 0, errno
		}
		if dest == nil {
			return len(name), nil
		}
		return copy(dest, name), nil
	}
	xattrSys.get = func(_, _ string, dest []byte, _ bool) (int, error) {
		if op == "get" {
			return 0, errno
		}
		if dest == nil {
			return 1, nil
		}
		return copy(dest, "v"), nil
	}
	xattrSys.set = func(_, _ string, _ []byte, _ bool) error {
		if op == "set" {
			return errno
		}
		return nil
	}
}

var unsupportedErrnos = map[string]error{
	"ENOTSUP":    unix.ENOTSUP,
	"EOPNOTSUPP": unix.EOPNOTSUPP,
	"ENOSYS":     unix.ENOSYS,
}

var xattrOps = []string{"list", "get", "set"}

// TestUnsupportedXattrsAreNoXattrs: a filesystem that does not implement xattrs,
// on either side, has none to preserve. The copy succeeds silently.
func TestUnsupportedXattrsAreNoXattrs(t *testing.T) {
	for errName, errno := range unsupportedErrnos {
		for _, op := range xattrOps {
			t.Run(op+"/"+errName, func(t *testing.T) {
				failXattr(t, op, errno)
				dir := t.TempDir()

				src := filepath.Join(dir, "src")
				require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
				info, err := os.Lstat(src)
				require.NoError(t, err)
				dst := filepath.Join(dir, "dst")
				n, err := CopyFile(vfs.Local, src, vfs.Local, dst, info, CopyOptions{Preserve: true})
				require.NoError(t, err)
				assert.Equal(t, int64(7), n)
				got, err := os.ReadFile(dst)
				require.NoError(t, err)
				assert.Equal(t, "payload", string(got))

				srcDir := filepath.Join(dir, "srcdir")
				dstDir := filepath.Join(dir, "dstdir")
				require.NoError(t, os.Mkdir(srcDir, 0o755))
				require.NoError(t, os.Mkdir(dstDir, 0o700))
				dinfo, err := os.Lstat(srcDir)
				require.NoError(t, err)
				require.NoError(t, ApplyMeta(vfs.Local, srcDir, vfs.Local, dstDir, dinfo, true))
			})
		}
	}
}

// TestSkippedXattrIsReported: a skipped attribute reaches the report with its
// path and errno, so --verbose can say what the copy left behind.
func TestSkippedXattrIsReported(t *testing.T) {
	t.Serial()
	for _, op := range xattrOps {
		t.Run(op, func(t *testing.T) {
			failXattr(t, op, unix.ENOTSUP)
			var got []error
			SetXattrSkipReport(func(path, name string, err error) { got = append(got, err) })
			t.Cleanup(func() { SetXattrSkipReport(nil) })
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
			info, err := os.Lstat(src)
			require.NoError(t, err)
			_, err = CopyFile(vfs.Local, src, vfs.Local, filepath.Join(dir, "dst"), info, CopyOptions{Preserve: true})
			require.NoError(t, err)
			require.Len(t, got, 1, "each skip is reported once")
			assert.ErrorIs(t, got[0], unix.ENOTSUP)
		})
	}
}

// TestXattrFailureKeepsFileData: any other xattr errno is reported, never
// swallowed, but a file's copied data is still published.
func TestXattrFailureKeepsFileData(t *testing.T) {
	for _, op := range xattrOps {
		t.Run(op, func(t *testing.T) {
			failXattr(t, op, unix.EIO)
			dir := t.TempDir()

			src := filepath.Join(dir, "src")
			require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
			info, err := os.Lstat(src)
			require.NoError(t, err)
			dst := filepath.Join(dir, "dst")
			n, err := CopyFile(vfs.Local, src, vfs.Local, dst, info, CopyOptions{Preserve: true})
			var me *MetaError
			require.ErrorAs(t, err, &me)
			assert.ErrorIs(t, err, unix.EIO)
			assert.Equal(t, dst, me.Path)
			assert.Equal(t, int64(7), n)
			got, err := os.ReadFile(dst)
			require.NoError(t, err)
			assert.Equal(t, "payload", string(got))

			srcDir := filepath.Join(dir, "srcdir")
			dstDir := filepath.Join(dir, "dstdir")
			require.NoError(t, os.Mkdir(srcDir, 0o755))
			require.NoError(t, os.Mkdir(dstDir, 0o700))
			dinfo, err := os.Lstat(srcDir)
			require.NoError(t, err)
			assert.ErrorIs(t, ApplyMeta(vfs.Local, srcDir, vfs.Local, dstDir, dinfo, true), unix.EIO)
		})
	}
}

// TestUnsupportedMetadataErrnos: each errno a filesystem uses to say "not
// implemented" skips chmod, chown, and chtimes silently.
func TestUnsupportedMetadataErrnos(t *testing.T) {
	for name, errno := range unsupportedErrnos {
		t.Run(name, func(t *testing.T) {
			assertMetaSkipped(t, &os.PathError{Op: "chmod", Path: "p", Err: errno})
		})
	}
}

// TestIgnorableXattrErr pins exactly which errno classes mean "no xattrs".
func TestIgnorableXattrErr(t *testing.T) {
	for name, errno := range unsupportedErrnos {
		assert.True(t, ignorableXattrErr(errno), name)
		assert.True(t, ignorableXattrErr(&os.PathError{Op: "listxattr", Path: "p", Err: errno}), name+" wrapped")
	}
	for _, errno := range []error{unix.EIO, unix.ENOENT, unix.EINVAL, unix.ENOSPC, unix.ERANGE, errors.New("x")} {
		assert.False(t, ignorableXattrErr(errno), "%v must not be swallowed", errno)
	}
}
