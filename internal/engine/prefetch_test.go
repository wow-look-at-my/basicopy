package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// slowListingFS delays every directory listing: the shape of a high-latency
// filesystem with good throughput. It counts listings and the peak number in
// progress so a test can prove the crawl overlapped round trips instead of
// paying one per directory.
type slowListingFS struct {
	vfs.FS
	delay  time.Duration
	count  atomic.Int64
	active atomic.Int64
	peak   atomic.Int64
}

func (s *slowListingFS) ReadDir(name string) ([]os.FileInfo, error) {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		peak := s.peak.Load()
		if n <= peak || s.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	s.count.Add(1)
	time.Sleep(s.delay)
	return s.FS.ReadDir(name)
}

// slowListings swaps the local filesystem seam for the duration of a test.
func slowListings(t *testing.T, delay time.Duration) *slowListingFS {
	t.Helper()
	t.Serial()
	wrapped := &slowListingFS{FS: vfs.Local, delay: delay}
	old := vfs.Local
	vfs.Local = wrapped
	t.Cleanup(func() { vfs.Local = old })
	return wrapped
}

// failingListFS refuses one directory listing. It stands in for an unreadable
// directory so the error path is deterministic without relying on permissions.
type failingListFS struct {
	vfs.FS
	path string
}

func (s failingListFS) ReadDir(name string) ([]os.FileInfo, error) {
	if name == s.path {
		return nil, os.ErrPermission
	}
	return s.FS.ReadDir(name)
}

// TestPrefetchedListingErrorIsReported keeps the failure accounting of a
// listing unchanged when the listing ran on the prefetch pool: the directory
// counts as one failure, the rest of the tree still copies, and the error names
// the directory.
func TestPrefetchedListingErrorIsReported(t *testing.T) {
	t.Serial()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("keep"), 0o644)
	writeFile(t, filepath.Join(src, "blocked", "hidden.txt"), []byte("x"), 0o644)

	old := vfs.Local
	vfs.Local = failingListFS{FS: old, path: filepath.Join(src, "blocked")}
	t.Cleanup(func() { vfs.Local = old })

	sum, _, errOut := captureRun(t, &options.Options{
		Sources:   []string{src},
		TargetDir: dst,
		Progress:  "never",
	})
	assert.EqualValues(t, 1, sum.Failed)
	assert.Contains(t, errOut, "read dir")
	assertSameContent(t, filepath.Join(src, "keep.txt"), filepath.Join(dst, "src", "keep.txt"))
}

// TestCrawlOverlapsDirectoryListings is the regression test for a crawl that
// pays one listing round trip per directory: a wide tree on a slow-listing
// filesystem must finish in well under the serial sum of its listing delays,
// with a bounded number of listings in flight and every file copied.
func TestCrawlOverlapsDirectoryListings(t *testing.T) {
	const (
		dirs    = 24
		delay   = 50 * time.Millisecond
		perDir  = 1
		wantSum = dirs * perDir
	)
	slow := slowListings(t, delay)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for i := 0; i < dirs; i++ {
		for j := 0; j < perDir; j++ {
			writeFile(t, filepath.Join(src, fmt.Sprintf("d%02d", i), fmt.Sprintf("f%d", j)), []byte("payload"), 0o644)
		}
	}

	o := &options.Options{
		Sources:   []string{src},
		TargetDir: dst,
		Progress:  "never",
	}
	start := time.Now()
	sum, _, _ := captureRun(t, o)
	elapsed := time.Since(start)

	require.EqualValues(t, wantSum, sum.Files)
	require.Zero(t, sum.Failed)
	for i := 0; i < dirs; i++ {
		for j := 0; j < perDir; j++ {
			name := filepath.Join(fmt.Sprintf("d%02d", i), fmt.Sprintf("f%d", j))
			assertSameContent(t, filepath.Join(src, name), filepath.Join(dst, "src", name))
		}
	}

	listings := slow.count.Load()
	require.Positive(t, listings)
	serial := time.Duration(listings) * delay
	peak := slow.peak.Load()
	assert.GreaterOrEqual(t, peak, int64(2),
		"listings must overlap (peak %d in flight)", peak)
	assert.LessOrEqual(t, peak, int64(listingWorkerCount()),
		"listings in flight must stay within the automatic cap")
	assert.Less(t, elapsed, serial/2,
		"crawl must beat the serial listing bound (took %v; serial %v for %d listings)",
		elapsed, serial, listings)
}
