//go:build unix

package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileOwner(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	fi, err := os.Lstat(p)
	require.NoError(t, err)
	uid, gid, ok := FileOwner(fi)
	require.True(t, ok)
	assert.Equal(t, os.Getuid(), uid)
	assert.Equal(t, os.Getgid(), gid)
}

// TestCompareOwnerDrift (root only): an unchanged file with a chowned
// destination reports the owner pair for the touch-up itemization.
func TestCompareOwnerDrift(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("chown to another uid needs root")
	}
	dir := t.TempDir()
	mt := time.Unix(1_700_000_000, 0)
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	si := write(t, src, []byte("hello"), mt)
	write(t, dst, []byte("hello"), mt)
	require.NoError(t, os.Lchown(dst, 12345, 54321))

	v := Compare(src, si, dst, false)
	assert.False(t, v.NeedCopy)
	assert.True(t, v.OwnerDiff)
	assert.Equal(t, 0, v.SrcUID)
	assert.Equal(t, 0, v.SrcGID)
	assert.Equal(t, 12345, v.DstUID)
	assert.Equal(t, 54321, v.DstGID)
}
