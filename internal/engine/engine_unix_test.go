//go:build unix

package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"golang.org/x/sys/unix"
)

// TestNoSpaceAbortsRun checks that a full destination (ENOSPC) aborts the whole
// run rather than being isolated as a single-file failure -- otherwise a backup
// to a too-small target would pointlessly fail every remaining file.
func TestNoSpaceAbortsRun(t *testing.T) {
	r := &runner{opts: &options.Options{}, stderr: io.Discard}
	r.fail(fmt.Errorf("copy big.bin: %w", unix.ENOSPC))
	assert.Error(t, r.abortErr(), "a full destination must abort the run")
	assert.EqualValues(t, 1, r.failed.Load())
}

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

// hardlinkPair creates src/h0.txt + src/h1.txt as one multiply-linked inode and
// returns validated options copying src into root/dst.
func hardlinkPair(t *testing.T) (o *options.Options, dst string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	a := filepath.Join(src, "h0.txt")
	writeFile(t, a, []byte("shared content"), 0o644)
	require.NoError(t, os.Link(a, filepath.Join(src, "h1.txt")))
	dst = filepath.Join(root, "dst")
	o = &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "auto"}
	require.NoError(t, o.Validate())
	return o, dst
}

func assertHardlinked(t *testing.T, a, b string) {
	t.Helper()
	s0, err := os.Stat(a)
	require.NoError(t, err)
	s1, err := os.Stat(b)
	require.NoError(t, err)
	assert.True(t, os.SameFile(s0, s1), "destination files must share one inode")
}

// TestHardlinkRelinkedToUnchangedPrimary reproduces the incremental-run bug where
// the first path to a linked inode is skipped as unchanged and therefore never
// recorded as the link primary: a secondary missing from the destination was then
// recopied as an independent duplicate instead of relinked, silently losing the
// hardlink structure (and doubling the stored data).
func TestHardlinkRelinkedToUnchangedPrimary(t *testing.T) {
	o, dst := hardlinkPair(t)
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	// Lose the secondary (e.g. a partial destination) and re-run.
	require.NoError(t, os.Remove(filepath.Join(dst, "src", "h1.txt")))
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 0, sum.Files, "nothing should be recopied")
	assert.EqualValues(t, 1, sum.Linked, "the missing path must be relinked to the kept copy")
	assertHardlinked(t, filepath.Join(dst, "src", "h0.txt"), filepath.Join(dst, "src", "h1.txt"))
}

// TestHardlinkRerunNoChurn checks the complementary case: a fully up-to-date
// destination with its links intact is left completely alone on re-run.
func TestHardlinkRerunNoChurn(t *testing.T) {
	o, dst := hardlinkPair(t)
	_, err := Run(context.Background(), o)
	require.NoError(t, err)

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sum.Files, "no copies on an up-to-date destination")
	assert.EqualValues(t, 0, sum.Linked, "no relinking of already-correct links")
	assert.EqualValues(t, 2, sum.Skipped, "both paths skip as unchanged")
	assertHardlinked(t, filepath.Join(dst, "src", "h0.txt"), filepath.Join(dst, "src", "h1.txt"))
}

// TestHardlinkRejoinsSeparatedCopies checks that a destination holding two
// identical but independent files (e.g. produced by an earlier --no-hardlinks
// copy) is restored to the source's link structure on a default re-run.
func TestHardlinkRejoinsSeparatedCopies(t *testing.T) {
	o, dst := hardlinkPair(t)
	first := *o
	first.NoHardlinks = true
	_, err := Run(context.Background(), &first)
	require.NoError(t, err)

	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sum.Files, "content is up to date; nothing recopied")
	assert.EqualValues(t, 1, sum.Linked, "the duplicate must be relinked to the primary")
	assertHardlinked(t, filepath.Join(dst, "src", "h0.txt"), filepath.Join(dst, "src", "h1.txt"))
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
