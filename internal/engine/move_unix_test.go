//go:build unix

package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

// An exclude that matches nothing turns off the whole-root rename, so these
// tests exercise the copy-then-remove path on one local filesystem.
var forceCopyPath = []string{"no-such-entry"}

func TestMoveCopyPathKeepsHardlinks(t *testing.T) {
	o, dst := hardlinkPair(t)
	o.Move = true
	o.Exclude = forceCopyPath
	src := o.Sources[0]

	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 1, sum.Linked)
	assertGone(t, src)
	assertHardlinked(t, filepath.Join(dst, "src", "h0.txt"), filepath.Join(dst, "src", "h1.txt"))
}

// A move takes symlinks as links, even without --no-follow-symlinks. Following an
// in-tree link would put its target at the destination twice.
func TestMoveTakesSymlinksAsLinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "real.txt"), []byte("data"), 0o644)
	require.NoError(t, os.Symlink("real.txt", filepath.Join(src, "link.txt")))

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Move: true, Exclude: forceCopyPath, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 1, sum.Symlinks)
	assertGone(t, src)
	target, err := os.Readlink(filepath.Join(dst, "src", "link.txt"))
	require.NoError(t, err)
	assert.Equal(t, "real.txt", target)
}

// A file source whose destination is itself (through a symlinked target
// directory) must survive: the skip-unchanged verdict sees identical content.
func TestMoveFileOntoItselfKeepsIt(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	writeFile(t, src, []byte("precious"), 0o644)
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(root, alias))

	o := &options.Options{Sources: []string{src}, TargetDir: alias, Move: true, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.EqualValues(t, 1, sum.Failed)
	assert.Zero(t, sum.Renamed)
	assert.Equal(t, "precious", readString(t, src))
}
