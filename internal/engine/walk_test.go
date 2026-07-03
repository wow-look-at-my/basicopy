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
