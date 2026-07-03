package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

func TestContentsCopiesIntoTargetDir(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Contents: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)

	assertSameContent(t, filepath.Join(src, "a.txt"), filepath.Join(dst, "a.txt"))
	assertSameContent(t, filepath.Join(src, "sub", "b.txt"), filepath.Join(dst, "sub", "b.txt"))
	_, statErr := os.Stat(filepath.Join(dst, "src"))
	assert.True(t, os.IsNotExist(statErr), "--contents must not nest under the source basename")
}

// TestContentsFileSourceStillNests: --contents changes directory sources only;
// a regular-file source still lands under its basename.
func TestContentsFileSourceStillNests(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "dir")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	file := filepath.Join(root, "lone.txt")
	writeFile(t, file, []byte("lone"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{Sources: []string{src, file}, TargetDir: dst, Contents: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assertSameContent(t, filepath.Join(src, "a.txt"), filepath.Join(dst, "a.txt"))
	assertSameContent(t, file, filepath.Join(dst, "lone.txt"))
}

func TestContentsRejectsTargetEqualsSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: src, Contents: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Failed, "copying a directory's contents onto itself must be rejected")
}

func TestContentsRejectsTargetInsideSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	dst := filepath.Join(src, "backup")

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Contents: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Failed)
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "the recursive target must be rejected before any mkdir")
}

// TestContentsAllowsTargetParentOfSource: with --contents the target being the
// source's parent is legal (rsync SRC/ PARENT/), unlike the nesting mode where
// DIR/<basename> would collide with the source itself.
func TestContentsAllowsTargetParentOfSource(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	src := filepath.Join(parent, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: parent, Contents: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assertSameContent(t, filepath.Join(src, "a.txt"), filepath.Join(parent, "a.txt"))
}

func TestContentsMirrorSingleSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(dst, "extra.txt"), []byte("x"), 0o644)
	writeFile(t, filepath.Join(dst, "junkdir", "j.txt"), []byte("j"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Contents: true, Mirror: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 2, sum.Deleted)

	assertSameContent(t, filepath.Join(src, "keep.txt"), filepath.Join(dst, "keep.txt"))
	_, e1 := os.Stat(filepath.Join(dst, "extra.txt"))
	assert.True(t, os.IsNotExist(e1), "extraneous file must be mirrored away from the target itself")
	_, e2 := os.Stat(filepath.Join(dst, "junkdir"))
	assert.True(t, os.IsNotExist(e2))
}

// TestContentsMirrorRejectsSourceInsideTarget: mirroring the target against a
// source that lives inside it would delete the source tree as "extraneous";
// the combination must be rejected up front.
func TestContentsMirrorRejectsSourceInsideTarget(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	src := filepath.Join(dst, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Contents: true, Mirror: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Failed)
	_, statErr := os.Stat(filepath.Join(src, "a.txt"))
	assert.NoError(t, statErr, "the source must survive untouched")
}

// TestExcludeTrailingSlashMatchesDirsOnly: rsync's 'node_modules/' syntax --
// the pattern prunes directories of that name at any depth but never matches a
// regular file of the same name.
func TestExcludeTrailingSlashMatchesDirsOnly(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	writeFile(t, filepath.Join(src, "node_modules", "dep.js"), []byte("d"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "node_modules", "nested.js"), []byte("n"), 0o644)
	writeFile(t, filepath.Join(src, "other", "node_modules"), []byte("i am a file"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Exclude: []string{"node_modules/"}, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)

	_, e1 := os.Stat(filepath.Join(dst, "src", "node_modules"))
	assert.True(t, os.IsNotExist(e1), "top-level node_modules dir must be pruned")
	_, e2 := os.Stat(filepath.Join(dst, "src", "sub", "node_modules"))
	assert.True(t, os.IsNotExist(e2), "nested node_modules dir must be pruned")
	assertSameContent(t, filepath.Join(src, "other", "node_modules"), filepath.Join(dst, "src", "other", "node_modules"))
	assertSameContent(t, filepath.Join(src, "keep.txt"), filepath.Join(dst, "src", "keep.txt"))
}

// TestExcludeWithoutSlashMatchesFilesToo: the plain pattern keeps its old
// behavior and matches files and directories alike.
func TestExcludeWithoutSlashMatchesFilesToo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	writeFile(t, filepath.Join(src, "node_modules", "dep.js"), []byte("d"), 0o644)
	writeFile(t, filepath.Join(src, "other", "node_modules"), []byte("i am a file"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Exclude: []string{"node_modules"}, Progress: "auto"}
	require.NoError(t, o.Validate())
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	_, e1 := os.Stat(filepath.Join(dst, "src", "node_modules"))
	assert.True(t, os.IsNotExist(e1))
	_, e2 := os.Stat(filepath.Join(dst, "src", "other", "node_modules"))
	assert.True(t, os.IsNotExist(e2), "without a trailing slash the file is excluded too")
}

// TestIncludeTrailingSlashDirOnly: the dir-only syntax works for --include as
// well -- a directory is re-included while a file of the same name is not.
func TestIncludeTrailingSlashDirOnly(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "vendor", "lib.js"), []byte("l"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "vendor"), []byte("plain file"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{
		Sources: []string{src}, TargetDir: dst,
		Exclude: []string{"vendor"}, Include: []string{"vendor/"},
		Progress: "auto",
	}
	require.NoError(t, o.Validate())
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	assertSameContent(t, filepath.Join(src, "vendor", "lib.js"), filepath.Join(dst, "src", "vendor", "lib.js"))
	_, statErr := os.Stat(filepath.Join(dst, "src", "sub", "vendor"))
	assert.True(t, os.IsNotExist(statErr), "the vendor FILE stays excluded; only the dir is re-included")
}

// TestExcludeTrailingSlashIgnoresSymlinkToDir: a dir-only pattern must not
// match a symlink even when its target is a directory -- the entry itself is a
// link, not a directory (Lstat semantics, matching rsync).
func TestExcludeTrailingSlashIgnoresSymlinkToDir(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "real_dir", "a.txt"), []byte("a"), 0o644)
	require.NoError(t, os.Symlink("real_dir", filepath.Join(src, "node_modules")))
	dst := filepath.Join(root, "dst")

	o := &options.Options{
		Sources: []string{src}, TargetDir: dst,
		Exclude: []string{"node_modules/"}, NoFollowSymlinks: true, Progress: "auto",
	}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 1, sum.Symlinks, "a symlink named node_modules must survive a dir-only exclude")
	tgt, err := os.Readlink(filepath.Join(dst, "src", "node_modules"))
	require.NoError(t, err)
	assert.Equal(t, "real_dir", tgt)
}

// TestMirrorMultiSourceSweepsEachRoot: plain --mirror with several sources
// still prunes extraneous entries under every per-source nested root.
func TestMirrorMultiSourceSweepsEachRoot(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	writeFile(t, filepath.Join(a, "keep_a.txt"), []byte("a"), 0o644)
	writeFile(t, filepath.Join(b, "keep_b.txt"), []byte("b"), 0o644)
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(dst, "a", "stale_a.txt"), []byte("x"), 0o644)
	writeFile(t, filepath.Join(dst, "b", "stale_b.txt"), []byte("y"), 0o644)

	o := &options.Options{Sources: []string{a, b}, TargetDir: dst, Mirror: true, Progress: "auto"}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 2, sum.Deleted)
	for _, p := range []string{filepath.Join(dst, "a", "stale_a.txt"), filepath.Join(dst, "b", "stale_b.txt")} {
		_, statErr := os.Stat(p)
		assert.True(t, os.IsNotExist(statErr), "extraneous %s must be mirrored away", p)
	}
	_, e1 := os.Stat(filepath.Join(dst, "a", "keep_a.txt"))
	assert.NoError(t, e1)
	_, e2 := os.Stat(filepath.Join(dst, "b", "keep_b.txt"))
	assert.NoError(t, e2)
}

// TestMirrorSkipsFailedSource: a source that fails its walk guards (here: it
// doesn't exist) must not have its destination swept -- with the old
// sources-derived sweep, one mistyped source emptied that source's whole
// destination tree.
func TestMirrorSkipsFailedSource(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	writeFile(t, filepath.Join(good, "keep.txt"), []byte("k"), 0o644)
	dst := filepath.Join(root, "dst")
	// Destination content whose source is missing (or was mistyped).
	writeFile(t, filepath.Join(dst, "gone", "precious.txt"), []byte("p"), 0o644)
	writeFile(t, filepath.Join(dst, "good", "stale.txt"), []byte("s"), 0o644)

	o := &options.Options{
		Sources:   []string{good, filepath.Join(root, "gone")},
		TargetDir: dst, Mirror: true, Progress: "auto",
	}
	require.NoError(t, o.Validate())
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Failed, "the missing source is a per-source failure")
	assert.EqualValues(t, 1, sum.Deleted, "only the healthy source's root is swept")
	_, e1 := os.Stat(filepath.Join(dst, "gone", "precious.txt"))
	assert.NoError(t, e1, "a failed source must never trigger a deletion sweep of its destination")
	_, e2 := os.Stat(filepath.Join(dst, "good", "stale.txt"))
	assert.True(t, os.IsNotExist(e2), "the healthy source still mirrors")
}
