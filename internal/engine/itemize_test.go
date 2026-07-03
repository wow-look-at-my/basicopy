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

// captureRun executes a full Run with the engine's output seams redirected into
// buffers, returning the summary plus everything written to stdout and stderr.
func captureRun(t *testing.T, o *options.Options) (*Summary, string, string) {
	t.Helper()
	require.NoError(t, o.Validate())
	var out, errOut bytes.Buffer
	oldOut, oldErr := runStdout, runStderr
	runStdout, runStderr = &out, &errOut
	defer func() { runStdout, runStderr = oldOut, oldErr }()
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	return sum, out.String(), errOut.String()
}

// TestDryRunItemizesByDefault: a dry run must print one reasoned line per
// pending change on stdout WITHOUT any verbosity flag -- that is the whole
// point of a dry run (rsync's silent bare --dry-run is the failure mode this
// avoids). Unchanged files stay silent unless --verbose.
func TestDryRunItemizesByDefault(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mt := time.Unix(1_700_000_000, 0)

	writeFile(t, filepath.Join(src, "new.txt"), []byte("fresh"), 0o644)
	writeFile(t, filepath.Join(src, "size.txt"), []byte("12345678"), 0o644)
	writeFile(t, filepath.Join(src, "mtime.txt"), []byte("hello"), 0o644)
	writeFile(t, filepath.Join(src, "type.txt"), []byte("file"), 0o644)
	writeFile(t, filepath.Join(src, "same.txt"), []byte("stable"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "inner.txt"), []byte("deep"), 0o644)
	for _, f := range []string{"mtime.txt", "same.txt"} {
		require.NoError(t, os.Chtimes(filepath.Join(src, f), mt, mt))
	}

	writeFile(t, filepath.Join(dst, "src", "size.txt"), []byte("old"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "mtime.txt"), []byte("world"), 0o644)
	require.NoError(t, os.Chtimes(filepath.Join(dst, "src", "mtime.txt"), mt.Add(2*time.Hour), mt.Add(2*time.Hour)))
	require.NoError(t, os.Symlink("size.txt", filepath.Join(dst, "src", "type.txt")))
	writeFile(t, filepath.Join(dst, "src", "same.txt"), []byte("stable"), 0o644)
	require.NoError(t, os.Chtimes(filepath.Join(dst, "src", "same.txt"), mt, mt))

	o := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Progress: "auto"}
	sum, out, _ := captureRun(t, o)

	d := filepath.Join(dst, "src")
	assert.Contains(t, out, fmt.Sprintf("would copy %s (new)\n", filepath.Join(d, "new.txt")))
	assert.Contains(t, out, fmt.Sprintf("would copy %s (size 3 -> 8)\n", filepath.Join(d, "size.txt")))
	assert.Contains(t, out, fmt.Sprintf("would copy %s (mtime differs)\n", filepath.Join(d, "mtime.txt")))
	assert.Contains(t, out, fmt.Sprintf("would copy %s (type change)\n", filepath.Join(d, "type.txt")))
	assert.Contains(t, out, fmt.Sprintf("would mkdir %s\n", filepath.Join(d, "sub")))
	assert.Contains(t, out, fmt.Sprintf("would copy %s (new)\n", filepath.Join(d, "sub", "inner.txt")))
	assert.NotContains(t, out, "same.txt", "an unchanged file must not be itemized")
	assert.NotContains(t, out, "skip unchanged", "skip lines are verbose-only")
	assert.NotContains(t, out, "would update", "no attribute drift anywhere in this tree")

	assert.EqualValues(t, 5, sum.Files)
	assert.EqualValues(t, 1, sum.Skipped)
	assert.EqualValues(t, 0, sum.Updated)

	// Verbose additionally shows the skip line.
	ov := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Verbose: true, Progress: "auto"}
	_, vout, _ := captureRun(t, ov)
	assert.Contains(t, vout, fmt.Sprintf("skip unchanged %s\n", filepath.Join(d, "same.txt")))
}

func TestDryRunQuietPrintsNothing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	o := &options.Options{Sources: []string{src}, TargetDir: filepath.Join(root, "dst"), DryRun: true, Quiet: true, Progress: "auto"}
	sum, out, _ := captureRun(t, o)
	assert.Empty(t, out, "--quiet must silence dry-run itemization")
	assert.EqualValues(t, 1, sum.Files, "counting is unaffected by --quiet")
}

func TestDryRunChecksumContentDiffers(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mt := time.Unix(1_700_000_000, 0)

	// Same size, same mtime, different content: only --checksum can see it.
	writeFile(t, filepath.Join(src, "sneaky.txt"), []byte("AAAA"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "sneaky.txt"), []byte("BBBB"), 0o644)
	require.NoError(t, os.Chtimes(filepath.Join(src, "sneaky.txt"), mt, mt))
	require.NoError(t, os.Chtimes(filepath.Join(dst, "src", "sneaky.txt"), mt, mt))

	o := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Checksum: true, Progress: "auto"}
	sum, out, _ := captureRun(t, o)
	assert.Contains(t, out, fmt.Sprintf("would copy %s (content differs)\n", filepath.Join(dst, "src", "sneaky.txt")))
	assert.EqualValues(t, 1, sum.Files)

	// Without --checksum the same tree itemizes nothing (the quick check passes).
	oq := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Progress: "auto"}
	sumq, outq, _ := captureRun(t, oq)
	assert.NotContains(t, outq, "sneaky.txt")
	assert.EqualValues(t, 0, sumq.Files)
	assert.EqualValues(t, 1, sumq.Skipped)
}

// TestAttrUpdateDryRun: an unchanged file whose permissions drifted is itemized
// as an attribute-only update (and counted), and the dry run must not fix it.
func TestAttrUpdateDryRun(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "f.txt"), []byte("payload"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	captureRun(t, o)
	require.NoError(t, os.Chmod(filepath.Join(dst, "src", "f.txt"), 0o600))

	od := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Progress: "auto"}
	sum, out, _ := captureRun(t, od)
	assert.Contains(t, out, fmt.Sprintf("would update %s (mode 0600 -> 0644)\n", filepath.Join(dst, "src", "f.txt")))
	assert.EqualValues(t, 1, sum.Updated)
	assert.EqualValues(t, 0, sum.Files)
	assert.EqualValues(t, 0, sum.Skipped)

	fi, err := os.Lstat(filepath.Join(dst, "src", "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "a dry run must not chmod")
}

// TestAttrUpdateRealRun: the same drift on a real run is repaired in place
// (chmod without a copy) and reported under --verbose.
func TestAttrUpdateRealRun(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "f.txt"), []byte("payload"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	captureRun(t, o)
	require.NoError(t, os.Chmod(filepath.Join(dst, "src", "f.txt"), 0o600))

	ov := &options.Options{Sources: []string{src}, TargetDir: dst, Verbose: true, Progress: "auto"}
	sum, out, _ := captureRun(t, ov)
	assert.Contains(t, out, fmt.Sprintf("update %s (mode 0600 -> 0644)\n", filepath.Join(dst, "src", "f.txt")))
	assert.EqualValues(t, 1, sum.Updated)
	assert.EqualValues(t, 0, sum.Files, "no copy for an attribute-only fix")

	fi, err := os.Lstat(filepath.Join(dst, "src", "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm(), "drifted permissions must be repaired on skip")
}

// TestAttrUpdateRespectsNoPreserve: with --no-preserve there is no metadata
// contract, so drifted attributes are left alone and the file plain-skips.
func TestAttrUpdateRespectsNoPreserve(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "f.txt"), []byte("payload"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	captureRun(t, o)
	require.NoError(t, os.Chmod(filepath.Join(dst, "src", "f.txt"), 0o600))

	on := &options.Options{Sources: []string{src}, TargetDir: dst, NoPreserve: true, Progress: "auto"}
	sum, _, _ := captureRun(t, on)
	assert.EqualValues(t, 0, sum.Updated)
	assert.EqualValues(t, 1, sum.Skipped)
	fi, err := os.Lstat(filepath.Join(dst, "src", "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

// TestChecksumMtimeTouchup: under --checksum a content-identical file with a
// drifted mtime gets a time touch-up instead of a pointless recopy -- itemized
// in a dry run, applied via Chtimes in a real one.
func TestChecksumMtimeTouchup(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mt := time.Unix(1_700_000_000, 0)
	writeFile(t, filepath.Join(src, "f.txt"), []byte("same bytes"), 0o644)
	require.NoError(t, os.Chtimes(filepath.Join(src, "f.txt"), mt, mt))
	writeFile(t, filepath.Join(dst, "src", "f.txt"), []byte("same bytes"), 0o644)
	require.NoError(t, os.Chtimes(filepath.Join(dst, "src", "f.txt"), mt.Add(3*time.Hour), mt.Add(3*time.Hour)))

	od := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Checksum: true, Progress: "auto"}
	sum, out, _ := captureRun(t, od)
	assert.Contains(t, out, fmt.Sprintf("would update %s (mtime)\n", filepath.Join(dst, "src", "f.txt")))
	assert.EqualValues(t, 1, sum.Updated)
	assert.EqualValues(t, 0, sum.Files)

	or := &options.Options{Sources: []string{src}, TargetDir: dst, Checksum: true, Progress: "auto"}
	sumr, _, _ := captureRun(t, or)
	assert.EqualValues(t, 1, sumr.Updated)
	assert.EqualValues(t, 0, sumr.Files)
	fi, err := os.Lstat(filepath.Join(dst, "src", "f.txt"))
	require.NoError(t, err)
	assert.WithinDuration(t, mt, fi.ModTime(), time.Second, "the real run must touch the mtime up to the source's")

	// Default (non-checksum) mode never reports a bare mtime touch-up: the
	// same drift is a full recopy reason instead.
	require.NoError(t, os.Chtimes(filepath.Join(dst, "src", "f.txt"), mt.Add(3*time.Hour), mt.Add(3*time.Hour)))
	oq := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Progress: "auto"}
	_, outq, _ := captureRun(t, oq)
	assert.Contains(t, outq, "(mtime differs)")
	assert.NotContains(t, outq, "would update")
}

// TestDirUpdateItemized: an existing destination directory with drifted mode is
// itemized as an update; a directory differing only by mtime stays silent
// (rsync's noisiest, least useful line).
func TestDirUpdateItemized(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "modedir", "a.txt"), []byte("x"), 0o644)
	writeFile(t, filepath.Join(src, "timedir", "b.txt"), []byte("y"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	captureRun(t, o)
	require.NoError(t, os.Chmod(filepath.Join(dst, "src", "modedir"), 0o700))
	drift := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dst, "src", "timedir"), drift, drift))

	od := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, Progress: "auto"}
	sum, out, _ := captureRun(t, od)
	assert.Contains(t, out, fmt.Sprintf("would update %s (mode 0700 -> 0755)\n", filepath.Join(dst, "src", "modedir")))
	assert.NotContains(t, out, "timedir", "pure-mtime directory drift must not be itemized")
	assert.EqualValues(t, 1, sum.Updated)
}

// TestRealRunVerboseFormats: a real run under --verbose uses the same itemized
// formats without the "would" prefix.
func TestRealRunVerboseFormats(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	require.NoError(t, os.Symlink("a.txt", filepath.Join(src, "l.lnk")))

	o := &options.Options{
		Sources: []string{src}, TargetDir: dst,
		NoFollowSymlinks: true, Verbose: true, Progress: "never",
	}
	sum, out, _ := captureRun(t, o)
	assert.Contains(t, out, fmt.Sprintf("mkdir %s\n", filepath.Join(dst, "src")))
	assert.Contains(t, out, fmt.Sprintf("copy %s (new)\n", filepath.Join(dst, "src", "a.txt")))
	assert.Contains(t, out, fmt.Sprintf("link %s -> a.txt\n", filepath.Join(dst, "src", "l.lnk")))
	assert.NotContains(t, out, "would ", "real runs never print the dry-run prefix")
	assert.EqualValues(t, 1, sum.Files)
	assert.EqualValues(t, 1, sum.Symlinks)

	// Default (non-verbose) real runs keep stdout quiet.
	require.NoError(t, os.RemoveAll(dst))
	oq := &options.Options{Sources: []string{src}, TargetDir: dst, NoFollowSymlinks: true, Progress: "never"}
	_, outq, _ := captureRun(t, oq)
	assert.Empty(t, outq)
}

// TestSymlinkSkipAndRelink: an existing identical symlink is a skip; a symlink
// whose target changed is itemized and relinked.
func TestSymlinkSkipAndRelink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hi"), 0o644)
	writeFile(t, filepath.Join(src, "b.txt"), []byte("ho"), 0o644)
	require.NoError(t, os.Symlink("a.txt", filepath.Join(src, "l.lnk")))

	o := &options.Options{Sources: []string{src}, TargetDir: dst, NoFollowSymlinks: true, Progress: "auto"}
	sum1, _, _ := captureRun(t, o)
	assert.EqualValues(t, 1, sum1.Symlinks)

	// Unchanged link -> skip on both dry and real reruns.
	od := &options.Options{Sources: []string{src}, TargetDir: dst, DryRun: true, NoFollowSymlinks: true, Progress: "auto"}
	sumd, outd, _ := captureRun(t, od)
	assert.EqualValues(t, 0, sumd.Symlinks)
	assert.EqualValues(t, 3, sumd.Skipped, "both files and the identical link skip")
	assert.NotContains(t, outd, "would link")

	sum2, _, _ := captureRun(t, o)
	assert.EqualValues(t, 0, sum2.Symlinks)
	assert.EqualValues(t, 3, sum2.Skipped)

	// Retarget the source link: the dry run itemizes it, the real run fixes it.
	require.NoError(t, os.Remove(filepath.Join(src, "l.lnk")))
	require.NoError(t, os.Symlink("b.txt", filepath.Join(src, "l.lnk")))

	sumd2, outd2, _ := captureRun(t, od)
	assert.Contains(t, outd2, fmt.Sprintf("would link %s -> b.txt\n", filepath.Join(dst, "src", "l.lnk")))
	assert.EqualValues(t, 1, sumd2.Symlinks)

	sum3, _, _ := captureRun(t, o)
	assert.EqualValues(t, 1, sum3.Symlinks)
	tgt, err := os.Readlink(filepath.Join(dst, "src", "l.lnk"))
	require.NoError(t, err)
	assert.Equal(t, "b.txt", tgt)
}

// TestMirrorDryRunItemizesDeletes: --mirror --dry-run prints its pending
// deletions by default (no --verbose needed).
func TestMirrorDryRunItemizesDeletes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "extra.txt"), []byte("x"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "keep.txt"), []byte("k"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: dst, Mirror: true, DryRun: true, Progress: "auto"}
	sum, out, _ := captureRun(t, o)
	assert.Contains(t, out, fmt.Sprintf("would delete %s\n", filepath.Join(dst, "src", "extra.txt")))
	assert.EqualValues(t, 1, sum.Deleted)
	_, err := os.Lstat(filepath.Join(dst, "src", "extra.txt"))
	assert.NoError(t, err, "dry run must not delete")
}
