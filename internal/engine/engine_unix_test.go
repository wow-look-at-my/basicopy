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
