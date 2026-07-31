package engine

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// fakeRemote makes "pc:" reachable for the duration of a test by answering
// connection attempts with an in-process SFTP server over pipes. The engine, the
// copy primitives, and the scan comparison all run their real remote code paths
// -- real SFTP packets, real handles, real attribute round trips -- so what these
// tests exercise differs from a copy to an actual host only in the transport
// underneath.
func fakeRemote(t *testing.T) {
	t.Helper()
	old := vfs.StartTransport
	vfs.StartTransport = func(vfs.Location, bool) (*vfs.Transport, error) {
		srvIn, cliOut := io.Pipe()
		cliIn, srvOut := io.Pipe()
		server, err := sftp.NewServer(struct {
			io.Reader
			io.WriteCloser
		}{srvIn, srvOut})
		if err != nil {
			return nil, err
		}
		go func() {
			_ = server.Serve()
			_ = server.Close()
		}()
		return &vfs.Transport{Stdout: cliIn, Stdin: cliOut, Close: func() error {
			_ = server.Close()
			_ = srvIn.Close()
			_ = cliIn.Close()
			return nil
		}}, nil
	}
	t.Cleanup(func() { vfs.StartTransport = old })
}

// TestCopyToRemoteDestination is the headline case: basicopy SRC --target-dir
// pc:/path. Content, permissions, and mtimes must land on the far side, and
// every reported path must name the host so the output is unambiguous.
func TestCopyToRemoteDestination(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mt := time.Unix(1_700_000_000, 0)

	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o640)
	writeFile(t, filepath.Join(src, "sub", "b.bin"), make([]byte, 300_000), 0o644)
	require.NoError(t, os.Chtimes(filepath.Join(src, "a.txt"), mt, mt))

	o := &options.Options{
		Sources:   []string{src},
		TargetDir: "pc:" + dst,
		Verbose:   true,
		Progress:  "never",
	}
	sum, out, _ := captureRun(t, o)

	assert.EqualValues(t, 2, sum.Files)
	assert.Zero(t, sum.Failed)
	assert.EqualValues(t, 300_005, sum.Bytes)

	got, err := os.ReadFile(filepath.Join(dst, "src", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "alpha", string(got))

	fi, err := os.Lstat(filepath.Join(dst, "src", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm(), "mode must be preserved across SFTP")
	assert.WithinDuration(t, mt, fi.ModTime(), time.Second, "mtime must be preserved (to SFTP's one-second resolution)")

	big, err := os.Lstat(filepath.Join(dst, "src", "sub", "b.bin"))
	require.NoError(t, err)
	assert.EqualValues(t, 300_000, big.Size(), "a file larger than one packet must copy whole")

	assert.Contains(t, out, "copy pc:"+filepath.Join(dst, "src", "a.txt"))

	// No temp file may survive a successful copy.
	entries, err := os.ReadDir(filepath.Join(dst, "src"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".basicopy-tmp-")
	}
}

// TestCopyFromRemoteSource is the other direction: pc:/path as SRC.
func TestCopyFromRemoteSource(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")

	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)

	o := &options.Options{
		Sources:   []string{"pc:" + src},
		TargetDir: dst,
		Progress:  "never",
	}
	sum, _, _ := captureRun(t, o)

	assert.EqualValues(t, 2, sum.Files)
	assert.Zero(t, sum.Failed)
	got, err := os.ReadFile(filepath.Join(dst, "src", "sub", "b.txt"))
	require.NoError(t, err)
	assert.Equal(t, "beta", string(got))
}

// TestRemoteTargetFile covers --target-file with a remote destination, including
// creating the missing parent directory on the far side.
func TestRemoteTargetFile(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	writeFile(t, src, []byte("exact"), 0o644)
	dst := filepath.Join(root, "new", "dir", "out.txt")

	o := &options.Options{
		Sources:    []string{src},
		TargetFile: "pc:" + dst,
		Progress:   "never",
	}
	sum, _, errOut := captureRun(t, o)

	assert.EqualValues(t, 1, sum.Files)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "exact", string(got))
	assert.Contains(t, errOut, "created directory pc:"+filepath.Join(root, "new"))
}

// TestRemoteIncrementalRerun: the second run of the same copy must move nothing.
// This is the property that makes remote copies usable at all -- without it
// every re-run would re-send the whole tree over the wire.
func TestRemoteIncrementalRerun(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	writeFile(t, filepath.Join(src, "b.txt"), []byte("beta"), 0o644)

	o := &options.Options{Sources: []string{src}, TargetDir: "pc:" + dst, Progress: "never"}
	sum, _, _ := captureRun(t, o)
	require.EqualValues(t, 2, sum.Files)

	sum, _, _ = captureRun(t, o)
	assert.Zero(t, sum.Files, "an unchanged tree must not be recopied")
	assert.EqualValues(t, 2, sum.Skipped)
	assert.Zero(t, sum.Failed)

	// And with --checksum, which hashes both ends over the wire.
	o.Checksum = true
	sum, _, _ = captureRun(t, o)
	assert.Zero(t, sum.Files, "content-identical files must be skipped in checksum mode too")
	assert.EqualValues(t, 2, sum.Skipped)
}

// TestRemoteDryRunItemizes: the plan a remote copy prints must name the host on
// every destination path.
func TestRemoteDryRunItemizes(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "new.txt"), []byte("fresh"), 0o644)
	require.NoError(t, os.Symlink("new.txt", filepath.Join(src, "link")))

	o := &options.Options{
		Sources:          []string{src},
		TargetDir:        "pc:" + dst,
		DryRun:           true,
		NoFollowSymlinks: true,
		Progress:         "never",
	}
	sum, out, errOut := captureRun(t, o)

	assert.EqualValues(t, 1, sum.Files)
	assert.EqualValues(t, 1, sum.Symlinks)
	assert.Contains(t, out, "would copy pc:"+filepath.Join(dst, "src", "new.txt")+" (new)")
	assert.Contains(t, out, "would link pc:"+filepath.Join(dst, "src", "link")+" -> new.txt")
	assert.Contains(t, errOut, "would create directory pc:"+dst)

	_, err := os.Lstat(dst)
	assert.True(t, os.IsNotExist(err), "a dry run must not touch the remote host")
}

// TestRemoteMirrorDeletes covers --mirror against a remote destination: the
// extraneous-file sweep reads and deletes over SFTP.
func TestRemoteMirrorDeletes(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("keep"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "stale.txt"), []byte("stale"), 0o644)
	writeFile(t, filepath.Join(dst, "src", "stale-dir", "inner.txt"), []byte("x"), 0o644)

	o := &options.Options{
		Sources:   []string{src},
		TargetDir: "pc:" + dst,
		Mirror:    true,
		Progress:  "never",
	}
	sum, _, _ := captureRun(t, o)

	assert.EqualValues(t, 2, sum.Deleted)
	assert.Zero(t, sum.Failed)
	_, err := os.Lstat(filepath.Join(dst, "src", "stale.txt"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(dst, "src", "keep.txt"))
	assert.NoError(t, err)
}

// TestRemoteSymlinksAndExcludes checks the walk-goroutine decisions (symlink
// recreation, exclude patterns) against a remote source, where they run on
// POSIX paths from SFTP rather than local ones.
func TestRemoteSymlinksAndExcludes(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "keep.txt"), []byte("keep"), 0o644)
	writeFile(t, filepath.Join(src, "skip.o"), []byte("object"), 0o644)
	writeFile(t, filepath.Join(src, "build", "junk.txt"), []byte("junk"), 0o644)
	require.NoError(t, os.Symlink("keep.txt", filepath.Join(src, "link")))

	o := &options.Options{
		Sources:          []string{"pc:" + src},
		TargetDir:        dst,
		Exclude:          []string{"*.o", "build/"},
		NoFollowSymlinks: true,
		Progress:         "never",
	}
	sum, _, _ := captureRun(t, o)

	assert.EqualValues(t, 1, sum.Files)
	assert.EqualValues(t, 1, sum.Symlinks)
	target, err := os.Readlink(filepath.Join(dst, "src", "link"))
	require.NoError(t, err)
	assert.Equal(t, "keep.txt", target)
	_, err = os.Lstat(filepath.Join(dst, "src", "skip.o"))
	assert.True(t, os.IsNotExist(err), "--exclude must apply to a remote source")
	_, err = os.Lstat(filepath.Join(dst, "src", "build"))
	assert.True(t, os.IsNotExist(err), "a directory-only pattern must apply to a remote source")
}

// TestRemoteHardlinkWarning: SFTP carries no inode identity, so hardlinked
// source files cannot be recognised. That changes what lands in the
// destination, so it must be said rather than silently absorbed.
func TestRemoteHardlinkWarning(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("shared"), 0o644)
	require.NoError(t, os.Link(filepath.Join(src, "a.txt"), filepath.Join(src, "b.txt")))

	o := &options.Options{Sources: []string{"pc:" + src}, TargetDir: dst, Verbose: true, Progress: "never"}
	sum, out, _ := captureRun(t, o)

	assert.EqualValues(t, 2, sum.Files, "both links are copied as ordinary files")
	assert.Zero(t, sum.Linked)

	// Said once under --verbose, however many files pass through, and never in
	// an ordinary run: it describes the protocol, not this tree.
	assert.Equal(t, 1, countSubstring(out, "no inode identity"))

	o.Verbose = false
	_, out, errOut := captureRun(t, o)
	assert.NotContains(t, out+errOut, "no inode identity")
}

// TestRemoteSpaceGuardUsesStatVFS: the pre-write space guard must work against a
// remote destination through SFTP's statvfs extension, not silently switch off.
func TestRemoteSpaceGuardUsesStatVFS(t *testing.T) {
	fakeRemote(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)

	reg := vfs.NewRegistry()
	defer reg.Close()
	r := &runner{opts: &options.Options{}, stderr: io.Discard, reg: reg}
	dfs, path, err := reg.Resolve("pc:" + root)
	require.NoError(t, err)
	r.dfs = dfs

	r.initSpaceGuard(path)
	require.True(t, r.spaceCheck, "a remote destination must report its free space")
	assert.Positive(t, r.freeBytes.Load())
	assert.True(t, r.reserveSpace(dfs.Join(path, "a.txt"), 1024))
}

func countSubstring(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
