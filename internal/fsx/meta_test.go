package fsx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncAttrs covers the attribute-only touch-up primitive: exactly the
// requested attributes are applied.
func TestSyncAttrs(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref")
	require.NoError(t, os.WriteFile(ref, []byte("r"), 0o644))
	mt := time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC)
	require.NoError(t, os.Chtimes(ref, mt, mt))
	info, err := os.Lstat(ref)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(dst, []byte("d"), 0o600))

	require.NoError(t, SyncAttrs(dst, info, true, true, true))
	fi, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
	assert.True(t, fi.ModTime().Equal(mt), "mtime must be synced, got %v", fi.ModTime())

	// A selective sync leaves unrequested attributes alone.
	require.NoError(t, os.Chmod(dst, 0o600))
	require.NoError(t, SyncAttrs(dst, info, false, false, true))
	fi, err = os.Lstat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "mode must not be touched when not requested")
}
