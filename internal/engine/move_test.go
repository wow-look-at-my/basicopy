package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

func assertGone(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	assert.True(t, os.IsNotExist(err), "%s must be gone after --move, got err=%v", path, err)
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestMoveRenamesOnSameFilesystem(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Move: true, Verbose: true, Progress: "never"}
	sum, out, _ := captureRun(t, o)

	assert.EqualValues(t, 1, sum.Renamed)
	assert.Zero(t, sum.Files, "a rename copies no bytes")
	assert.Zero(t, sum.Failed)
	assertGone(t, src)
	assert.Equal(t, "beta", readString(t, filepath.Join(dst, "src", "sub", "b.txt")))
	assert.Contains(t, out, fmt.Sprintf("rename %s -> %s", src, filepath.Join(dst, "src")))
}

func TestMoveTargetFileRenames(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "in.txt")
	dst := filepath.Join(root, "deep", "out.txt")
	writeFile(t, src, []byte("exact"), 0o644)

	sum, _, _ := captureRun(t, &options.Options{Sources: []string{src}, TargetFile: dst, Move: true, Progress: "never"})

	assert.EqualValues(t, 1, sum.Renamed)
	assertGone(t, src)
	assert.Equal(t, "exact", readString(t, dst))
}

// A target that already holds the tree cannot take a rename, so the move merges
// file by file: changed files copy, unchanged ones stay, every source goes.
func TestMoveMergesIntoExistingTarget(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "new.txt"), []byte("fresh"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "same.txt"), []byte("stable"), 0o644)
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "src", "sub"), 0o755))

	// The earliest run is a plain copy, which makes same.txt unchanged for the move.
	captureRun(t, &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "never"})
	writeFile(t, filepath.Join(src, "new.txt"), []byte("fresher"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Move: true, Verbose: true, Progress: "never"}
	sum, out, _ := captureRun(t, o)

	assert.Zero(t, sum.Renamed)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 1, sum.Files)
	assert.EqualValues(t, 4, sum.Removed, "two files and two directories")
	assertGone(t, src)
	assert.Equal(t, "fresher", readString(t, filepath.Join(dst, "src", "new.txt")))
	assert.Equal(t, "stable", readString(t, filepath.Join(dst, "src", "sub", "same.txt")))
	assert.Contains(t, out, fmt.Sprintf("move %s (size 5 -> 7)", filepath.Join(dst, "src", "new.txt")))
	assert.Contains(t, out, fmt.Sprintf("remove source %s (unchanged at %s)",
		filepath.Join(src, "sub", "same.txt"), filepath.Join(dst, "src", "sub", "same.txt")))
}

// An excluded entry stays in the source, and so does every directory above it.
func TestMoveKeepsExcludedEntries(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "keep", "x.o"), []byte("obj"), 0o644)
	writeFile(t, filepath.Join(src, "keep", "x.c"), []byte("code"), 0o644)
	writeFile(t, filepath.Join(src, "gone", "y.c"), []byte("more"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Move: true, Exclude: []string{"*.o"}, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed, "a directory kept by an exclude is not a failure")
	assert.Zero(t, sum.Renamed, "an exclude rules out the whole-root rename")
	assert.Equal(t, "obj", readString(t, filepath.Join(src, "keep", "x.o")))
	assertGone(t, filepath.Join(src, "keep", "x.c"))
	assertGone(t, filepath.Join(src, "gone"))
	assert.Equal(t, "code", readString(t, filepath.Join(dst, "src", "keep", "x.c")))
	_, err := os.Stat(filepath.Join(dst, "src", "keep", "x.o"))
	assert.True(t, os.IsNotExist(err))
}

func TestMoveContentsKeepsSourceRoot(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Contents: true, Move: true, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed)
	entries, err := os.ReadDir(src)
	require.NoError(t, err, "--contents moves what is inside the source, not the source itself")
	assert.Empty(t, entries)
	assert.Equal(t, "beta", readString(t, filepath.Join(dst, "sub", "b.txt")))
}

// --mirror reads the source to find extraneous entries, so the source removal
// must wait for it. Otherwise it deletes the tree it moved.
func TestMoveWithMirror(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "stale.txt"), []byte("old"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Mirror: true, Move: true, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 1, sum.Deleted)
	assertGone(t, src)
	assertGone(t, filepath.Join(dst, "src", "stale.txt"))
	assert.Equal(t, "alpha", readString(t, filepath.Join(dst, "src", "a.txt")))
}

// A target reached through a symlink to the source's own parent names the source
// files themselves. Removing them would delete the only copy.
func TestMoveRefusesToRemoveAliasedSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("precious"), 0o644)
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(root, alias))

	o := &options.Options{Sources: []string{src}, TargetDir: alias, Move: true, Progress: "never"}
	sum, _, errOut := captureRun(t, o)

	assert.EqualValues(t, 2, sum.Failed, "the file and its directory")
	assert.Zero(t, sum.Removed)
	assert.Equal(t, "precious", readString(t, filepath.Join(src, "a.txt")))
	assert.Contains(t, errOut, "it is the destination")
}

func TestMoveDryRunRemovesNothing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Move: true, DryRun: true, Progress: "never"}
	sum, out, _ := captureRun(t, o)

	assert.Zero(t, sum.Removed)
	assert.Zero(t, sum.Renamed)
	assert.Equal(t, "alpha", readString(t, filepath.Join(src, "a.txt")))
	assert.Contains(t, out, fmt.Sprintf("would move %s (new)", filepath.Join(dst, "src", "a.txt")))
}

// A move to another host cannot rename, so it copies and then removes the source.
func TestMoveToRemoteRemovesSource(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: "pc:" + dst, Move: true, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed)
	assert.Zero(t, sum.Renamed)
	assert.EqualValues(t, 2, sum.Files)
	assert.EqualValues(t, 4, sum.Removed)
	assertGone(t, src)
	assert.Equal(t, "beta", readString(t, filepath.Join(dst, "src", "sub", "b.txt")))
}

func TestMoveFromRemoteRemovesSource(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)

	o := &options.Options{Sources: []string{"pc:" + src}, TargetDir: dst, Move: true, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.Zero(t, sum.Failed)
	assertGone(t, src)
	assert.Equal(t, "beta", readString(t, filepath.Join(dst, "src", "sub", "b.txt")))
}

// Both ends on a single remote host: the far side does the rename, no bytes cross the wire.
func TestMoveRenamesOnRemoteHost(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)

	o := &options.Options{Sources: []string{"pc:" + src}, TargetDir: "pc:" + dst, Move: true, Progress: "never"}
	sum, _, _ := captureRun(t, o)

	assert.EqualValues(t, 1, sum.Renamed)
	assert.Zero(t, sum.Files)
	assertGone(t, src)
	assert.Equal(t, "alpha", readString(t, filepath.Join(dst, "src", "a.txt")))
}
