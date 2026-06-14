//go:build linux

package fsx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireACLTools(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("setfacl not available")
	}
	if _, err := exec.LookPath("getfacl"); err != nil {
		t.Skip("getfacl not available")
	}
	out, err := exec.Command("id", "-un").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func aclText(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("getfacl", "--absolute-names", "--omit-header", path).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestCopyFilePreservesLinuxACL(t *testing.T) {
	user := requireACLTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))
	require.NoError(t, exec.Command("setfacl", "-m", "u:"+user+":rw", src).Run())
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(dir, "dst")
	_, err = CopyFile(src, dst, info, CopyOptions{Preserve: true})
	require.NoError(t, err)
	assert.Equal(t, aclText(t, src), aclText(t, dst))
}

func TestApplyMetaPreservesLinuxDefaultACL(t *testing.T) {
	user := requireACLTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.Mkdir(src, 0o755))
	require.NoError(t, os.Mkdir(dst, 0o755))
	require.NoError(t, exec.Command("setfacl", "-m", "d:u:"+user+":rwx", src).Run())
	info, err := os.Lstat(src)
	require.NoError(t, err)

	require.NoError(t, ApplyMeta(src, dst, info, true))
	assert.Equal(t, aclText(t, src), aclText(t, dst))
}
