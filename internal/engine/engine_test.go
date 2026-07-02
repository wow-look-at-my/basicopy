package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

func writeFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, perm))
}

func assertSameContent(t *testing.T, want, got string) {
	t.Helper()
	dw, err := os.ReadFile(want)
	require.NoError(t, err)
	dg, err := os.ReadFile(got)
	require.NoError(t, err)
	assert.Equal(t, dw, dg, "content mismatch: %s vs %s", got, want)
}

func TestCopyTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello world"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.bin"), []byte{0, 1, 2, 3, 0xff}, 0o640)
	big := make([]byte, 200_000)
	for i := range big {
		big[i] = byte(i * 7)
	}
	writeFile(t, filepath.Join(src, "sub", "deep", "big.dat"), big, 0o644)
	require.NoError(t, os.Symlink("a.txt", filepath.Join(src, "link-in.txt")))
	outside := filepath.Join(root, "outside.txt")
	writeFile(t, outside, []byte("outside"), 0o644)
	require.NoError(t, os.Symlink(outside, filepath.Join(src, "link-out.txt")))

	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, NoSymlinkWarnings: true, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)

	assertSameContent(t, filepath.Join(src, "a.txt"), filepath.Join(dst, "src", "a.txt"))
	assertSameContent(t, filepath.Join(src, "sub", "b.bin"), filepath.Join(dst, "src", "sub", "b.bin"))
	assertSameContent(t, filepath.Join(src, "sub", "deep", "big.dat"), filepath.Join(dst, "src", "sub", "deep", "big.dat"))

	bi, err := os.Lstat(filepath.Join(dst, "src", "sub", "b.bin"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), bi.Mode().Perm())

	li, err := os.Lstat(filepath.Join(dst, "src", "link-in.txt"))
	require.NoError(t, err)
	assert.Zero(t, li.Mode()&os.ModeSymlink, "in-tree symlink should be dereferenced into a real file")

	lo, err := os.Lstat(filepath.Join(dst, "src", "link-out.txt"))
	require.NoError(t, err)
	assert.NotZero(t, lo.Mode()&os.ModeSymlink, "out-of-tree symlink should be preserved as a link")
	tgt, err := os.Readlink(filepath.Join(dst, "src", "link-out.txt"))
	require.NoError(t, err)
	assert.Equal(t, outside, tgt)
}

func TestDryRunCopiesNothing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"), 0o644)
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Files)
	_, statErr := os.Stat(filepath.Join(dst, "src", "a.txt"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create files")
}

func TestTargetFileWithDeepAutoMkdir(t *testing.T) {
	root := t.TempDir()
	srcFile := filepath.Join(root, "in", "data.txt")
	writeFile(t, srcFile, []byte("payload"), 0o644)
	dstFile := filepath.Join(root, "a", "b", "c", "out.txt")
	o := &options.Options{Sources: []string{srcFile}, TargetFile: dstFile, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assertSameContent(t, srcFile, dstFile)
}

func TestNoFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	require.NoError(t, os.Symlink("a.txt", filepath.Join(src, "link.txt")))
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, NoFollowSymlinks: true, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	li, err := os.Lstat(filepath.Join(dst, "src", "link.txt"))
	require.NoError(t, err)
	assert.NotZero(t, li.Mode()&os.ModeSymlink, "should be kept as a symlink under --no-follow-symlinks")
	tgt, err := os.Readlink(filepath.Join(dst, "src", "link.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a.txt", tgt)
}

func TestErrorIsolation(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good.txt")
	writeFile(t, good, []byte("ok"), 0o644)
	dst := filepath.Join(root, "dst")
	o := &options.Options{
		Sources:   []string{filepath.Join(root, "nope-does-not-exist"), good},
		TargetDir: dst,
		Progress:  "auto",
	}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err, "a bad source should not abort the run by default")
	assert.EqualValues(t, 1, sum.Failed)
	assertSameContent(t, good, filepath.Join(dst, "good.txt"))
}

func TestFatalErrorsAborts(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	o := &options.Options{
		Sources:     []string{filepath.Join(root, "nope")},
		TargetDir:   dst,
		FatalErrors: true,
		Progress:    "auto",
	}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	assert.Error(t, err, "--fatal-errors should surface the first error")
}

func TestDanglingSymlinkKept(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.Symlink("does-not-exist", filepath.Join(src, "dead.lnk")))
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	li, err := os.Lstat(filepath.Join(dst, "src", "dead.lnk"))
	require.NoError(t, err)
	assert.NotZero(t, li.Mode()&os.ModeSymlink, "dangling symlink must be kept as a link")
}

func TestSymlinkLoopTerminates(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	loop := filepath.Join(src, "loop")
	writeFile(t, filepath.Join(loop, "inner.txt"), []byte("x"), 0o644)
	require.NoError(t, os.Symlink(".", filepath.Join(loop, "self"))) // self-referential dir loop
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.NotZero(t, sum.Skipped, "the loop should be detected and skipped")
	assertSameContent(t, filepath.Join(loop, "inner.txt"), filepath.Join(dst, "src", "loop", "inner.txt"))
}

func TestNoAutoMkdirsErrors(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	o := &options.Options{
		Sources:      []string{src},
		TargetDir:    filepath.Join(root, "missing", "deep"),
		NoAutoMkdirs: true,
		Progress:     "auto",
	}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	assert.Error(t, err, "--no-auto-mkdirs must error on a missing target root")
}

func TestTargetFileRejectsDir(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	o := &options.Options{Sources: []string{src}, TargetFile: filepath.Join(root, "out"), Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	assert.Error(t, err, "--target-file with a directory source must error")
}

func TestSkipUnchangedOnRerun(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644)
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum1, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 2, sum1.Files)

	sum2, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sum2.Files, "re-run should copy nothing")
	assert.EqualValues(t, 2, sum2.Skipped, "re-run should skip both unchanged files")
}

// TestSkipUnchangedSymlinkTargetOnRerun guards the incremental path for
// dereferenced in-tree symlinks: the copy made for the symlink must get the same
// skip-unchanged treatment as a plain file, or every re-run recopies it.
func TestSkipUnchangedSymlinkTargetOnRerun(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "f.txt"), []byte("payload"), 0o644)
	require.NoError(t, os.Symlink("f.txt", filepath.Join(src, "s.txt")))
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum1, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 2, sum1.Files, "first run copies the file and the dereferenced link")

	sum2, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sum2.Files, "re-run should copy nothing")
	assert.EqualValues(t, 2, sum2.Skipped, "both the file and the dereferenced link are unchanged")
}

func TestExcludeFilter(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	writeFile(t, filepath.Join(src, "drop.tmp"), []byte("d"), 0o644)
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Exclude: []string{"*.tmp"}, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(dst, "src", "drop.tmp"))
	assert.True(t, os.IsNotExist(statErr), "*.tmp should be excluded")
	assertSameContent(t, filepath.Join(src, "keep.txt"), filepath.Join(dst, "src", "keep.txt"))
}

func TestIncludeOverridesExclude(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.log"), []byte("a"), 0o644)
	writeFile(t, filepath.Join(src, "keep.log"), []byte("k"), 0o644)
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Exclude: []string{"*.log"}, Include: []string{"keep.log"}, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	_, e1 := os.Stat(filepath.Join(dst, "src", "a.log"))
	assert.True(t, os.IsNotExist(e1), "a.log should be excluded")
	_, e2 := os.Stat(filepath.Join(dst, "src", "keep.log"))
	assert.NoError(t, e2, "keep.log should be re-included")
}

func TestMirrorDeletesExtraneous(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "in.txt"), []byte("i"), 0o644)
	dst := filepath.Join(root, "dst")

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	// Introduce extraneous content in the destination.
	writeFile(t, filepath.Join(dst, "src", "extra.txt"), []byte("x"), 0o644)
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "src", "extradir"), 0o755))
	writeFile(t, filepath.Join(dst, "src", "extradir", "junk"), []byte("j"), 0o644)

	om := &options.Options{Sources: []string{src}, TargetDir: dst, Mirror: true, Progress: "auto"}
	require.NoError(t, om.Validate())
	sum, err := Run(context.Background(), om)
	require.NoError(t, err)
	assert.NotZero(t, sum.Deleted)

	_, e1 := os.Stat(filepath.Join(dst, "src", "extra.txt"))
	assert.True(t, os.IsNotExist(e1), "extraneous file must be deleted")
	_, e2 := os.Stat(filepath.Join(dst, "src", "extradir"))
	assert.True(t, os.IsNotExist(e2), "extraneous dir must be deleted")
	_, e3 := os.Stat(filepath.Join(dst, "src", "keep.txt"))
	assert.NoError(t, e3, "matching file must be kept")
	_, e4 := os.Stat(filepath.Join(dst, "src", "sub", "in.txt"))
	assert.NoError(t, e4, "matching nested file must be kept")
}

func TestAutoscaleControllerRuns(t *testing.T) {
	old := controlInterval
	controlInterval = time.Millisecond
	oldW := watchInterval
	watchInterval = time.Millisecond
	defer func() { controlInterval = old; watchInterval = oldW }()

	root := t.TempDir()
	src := filepath.Join(root, "src")
	// Enough files and bytes that the copy spans several controller ticks.
	for d := 0; d < 10; d++ {
		for f := 0; f < 50; f++ {
			p := filepath.Join(src, fmt.Sprintf("d%d", d), fmt.Sprintf("f%d.bin", f))
			writeFile(t, p, bytes.Repeat([]byte{byte(f)}, 20_000), 0o644)
		}
	}
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 500, sum.Files)
}

func TestReserveSpaceDisabled(t *testing.T) {
	r := &runner{opts: &options.Options{}} // spaceCheck false -> guard off
	assert.True(t, r.reserveSpace("/dst/huge", 1<<40), "with no guard every file is allowed")
}

func TestReserveSpaceAbortsWhenFileWontFit(t *testing.T) {
	old := availBytes
	availBytes = func(string) (int64, bool) { return 100, true } // only 100 bytes free
	defer func() { availBytes = old }()

	r := &runner{opts: &options.Options{}, stderr: &bytes.Buffer{}, spaceCheck: true}
	r.freeBytes.Store(100)
	assert.False(t, r.reserveSpace("/dst/big.bin", 1<<20), "a file larger than free space must be refused")
	assert.Error(t, r.abortErr(), "and the run must abort rather than attempt the write")
}

func TestReserveSpaceConfirmsRoomViaStatfs(t *testing.T) {
	old := availBytes
	availBytes = func(string) (int64, bool) { return 1 << 30, true } // 1 GiB really free
	defer func() { availBytes = old }()

	r := &runner{opts: &options.Options{}, spaceCheck: true}
	r.freeBytes.Store(0) // estimate exhausted -> forces a real statfs, which finds room
	assert.True(t, r.reserveSpace("/dst/f", 4096))
	assert.NoError(t, r.abortErr())
}

func TestProgressAlwaysDraws(t *testing.T) {
	oldP := progressInterval
	progressInterval = time.Millisecond
	defer func() { progressInterval = oldP }()

	root := t.TempDir()
	src := filepath.Join(root, "src")
	for i := 0; i < 100; i++ {
		p := filepath.Join(src, fmt.Sprintf("f%d.bin", i))
		writeFile(t, p, bytes.Repeat([]byte{byte(i)}, 30_000), 0o644)
	}
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "always"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 100, sum.Files)
}

func TestVerboseRealCopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "sub", "a.txt"), []byte("hi"), 0o644)
	outside := filepath.Join(root, "outside.txt")
	writeFile(t, outside, []byte("o"), 0o644)
	require.NoError(t, os.Symlink(outside, filepath.Join(src, "out.lnk")))
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Verbose: true, NoSymlinkWarnings: true, Progress: "always"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assertSameContent(t, filepath.Join(src, "sub", "a.txt"), filepath.Join(dst, "src", "sub", "a.txt"))
}

func TestDryRunWithSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	require.NoError(t, os.Symlink("a.txt", filepath.Join(src, "in.lnk"))) // in-tree -> deref
	require.NoError(t, os.Symlink("/etc", filepath.Join(src, "out.lnk"))) // out-of-tree -> keep
	dst := filepath.Join(root, "deep", "dst")                             // forces auto-mkdir "would create"
	o := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Verbose: true, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create the target root")
}

func TestTargetRootUnderFileErrors(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	blocker := filepath.Join(root, "blocker")
	writeFile(t, blocker, []byte("i am a file"), 0o644)
	// Target root sits *under* a regular file -> mkdir must fail (ENOTDIR).
	o := &options.Options{Sources: []string{src}, TargetDir: filepath.Join(blocker, "sub"), Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	assert.Error(t, err, "creating a target root beneath a file must fail")
}

func TestRejectsDestinationInsideSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	dst := filepath.Join(src, "backup")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err, "recursive target should be isolated as a failed source")
	assert.EqualValues(t, 1, sum.Failed)
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "recursive target must be rejected before creating directories")
}

func TestRejectsDestinationSameAsSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	o := &options.Options{Sources: []string{src}, TargetDir: root, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err, "self-copy target should be isolated as a failed source")
	assert.EqualValues(t, 1, sum.Failed)
}

func TestCopyFailsOnDestCollision(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "x.txt"), []byte("payload"), 0o644)
	dst := filepath.Join(root, "dst")
	// Pre-create the destination path as a directory so the atomic rename collides.
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "src", "x.txt"), 0o755))
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())

	sum, err := Run(context.Background(), o)
	require.NoError(t, err, "a single file failure should be isolated, not fatal")
	assert.NotZero(t, sum.Failed, "copy onto an existing directory must be reported as failed")
}

func TestSymlinkToParentKept(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.Symlink("..", filepath.Join(src, "up"))) // resolves to root, exactly the parent
	dst := filepath.Join(root, "dst")
	o := &options.Options{Sources: []string{src}, TargetDir: dst, NoSymlinkWarnings: true, Progress: "auto"}
	require.NoError(t, o.Validate())

	_, err := Run(context.Background(), o)
	require.NoError(t, err)
	li, err := os.Lstat(filepath.Join(dst, "src", "up"))
	require.NoError(t, err)
	assert.NotZero(t, li.Mode()&os.ModeSymlink, "a link to the parent is out-of-tree and must be kept")
}
