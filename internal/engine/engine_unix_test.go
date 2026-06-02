//go:build unix

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/testify/assert"
	"github.com/wow-look-at-my/testify/require"
	"golang.org/x/sys/unix"
)

func TestSpecialFileSkipped(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	writeFile(t, filepath.Join(src, "real.txt"), []byte("data"), 0o644)
	require.NoError(t, unix.Mkfifo(filepath.Join(src, "pipe"), 0o644))

	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.NotZero(t, sum.Skipped, "the fifo should be skipped, not copied")
	assertSameContent(t, filepath.Join(src, "real.txt"), filepath.Join(dst, "src", "real.txt"))

	_, statErr := os.Stat(filepath.Join(dst, "src", "pipe"))
	assert.True(t, os.IsNotExist(statErr), "special file must not be recreated")
}

func TestHardlinkPreserved(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	a := filepath.Join(src, "h0.txt")
	writeFile(t, a, []byte("shared content"), 0o644)
	require.NoError(t, os.Link(a, filepath.Join(src, "h1.txt")))

	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 1, sum.Linked, "the second link should be preserved, not duplicated")

	s0, err := os.Stat(filepath.Join(dst, "src", "h0.txt"))
	require.NoError(t, err)
	s1, err := os.Stat(filepath.Join(dst, "src", "h1.txt"))
	require.NoError(t, err)
	assert.True(t, os.SameFile(s0, s1), "destination files must share one inode")
	assertSameContent(t, a, filepath.Join(dst, "src", "h0.txt"))
}

func TestNoHardlinksDuplicates(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	a := filepath.Join(src, "h0.txt")
	writeFile(t, a, []byte("data"), 0o644)
	require.NoError(t, os.Link(a, filepath.Join(src, "h1.txt")))

	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, NoHardlinks: true, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	s0, err := os.Stat(filepath.Join(dst, "src", "h0.txt"))
	require.NoError(t, err)
	s1, err := os.Stat(filepath.Join(dst, "src", "h1.txt"))
	require.NoError(t, err)
	assert.False(t, os.SameFile(s0, s1), "--no-hardlinks must produce independent files")
}
