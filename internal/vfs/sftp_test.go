package vfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteIdentity(t *testing.T) {
	fsys := testFS(t)
	assert.False(t, fsys.Local())
	assert.Equal(t, "somehost", fsys.Host())
	assert.Equal(t, "somehost:/srv/x", fsys.Display("/srv/x"))
	assert.Equal(t, "/", fsys.Separator())
}

func TestRemotePathHelpers(t *testing.T) {
	fsys := testFS(t)
	assert.Equal(t, "/a/b/c", fsys.Join("/a", "b", "c"))
	assert.Equal(t, "/a/b", fsys.Dir("/a/b/c"))
	assert.Equal(t, "c", fsys.Base("/a/b/c"))
	assert.Equal(t, "/a/c", fsys.Clean("/a/b/../c"))
	assert.True(t, fsys.IsAbs("/a"))
	assert.False(t, fsys.IsAbs("a"))

	abs, err := fsys.Abs("/a/b/../c")
	require.NoError(t, err)
	assert.Equal(t, "/a/c", abs)

	rel, err := fsys.Rel("/a/b", "/a/b/c/d")
	require.NoError(t, err)
	assert.Equal(t, "c/d", rel)
	rel, err = fsys.Rel("/a/b", "/a/b")
	require.NoError(t, err)
	assert.Equal(t, ".", rel)
	rel, err = fsys.Rel("/a/b", "/a/x")
	require.NoError(t, err)
	assert.Equal(t, "../x", rel)
	_, err = fsys.Rel("/a", "rel")
	assert.Error(t, err, "an absolute base and a relative target cannot be related")

	ok, err := fsys.Match("*.o", "main.o")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestRemoteAbsUsesLoginDirectory covers "host:notes": a relative remote path is
// resolved against the login directory, as scp and rsync do.
func TestRemoteAbsUsesLoginDirectory(t *testing.T) {
	fsys := testFS(t)
	wd, err := os.Getwd()
	require.NoError(t, err)

	abs, err := fsys.Abs("notes.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(wd, "notes.txt"), abs)
}

func TestRemoteFileLifecycle(t *testing.T) {
	fsys := testFS(t)
	dir := t.TempDir()

	// Create through the temp-file path the copy engine uses.
	f, err := fsys.CreateTemp(dir, ".basicopy-tmp-*")
	require.NoError(t, err)
	assert.Contains(t, f.Name(), ".basicopy-tmp-")
	_, err = f.Write([]byte("hello remote"))
	require.NoError(t, err)
	require.NoError(t, f.Chmod(0o640))
	require.NoError(t, f.Close())

	final := filepath.Join(dir, "file.txt")
	require.NoError(t, fsys.Rename(f.Name(), final))

	fi, err := fsys.Lstat(final)
	require.NoError(t, err)
	assert.EqualValues(t, len("hello remote"), fi.Size())
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm())

	// Read it back through the FS.
	in, err := fsys.Open(final)
	require.NoError(t, err)
	buf := make([]byte, 64)
	// A remote read legitimately returns data and io.EOF in the same call, which
	// every read loop in the copy path is written to handle.
	n, err := in.Read(buf)
	if err != nil {
		require.ErrorIs(t, err, io.EOF)
	}
	assert.Equal(t, "hello remote", string(buf[:n]))
	require.NoError(t, in.Close())

	// Rename must replace an existing destination -- the atomic-publish step of
	// every copy depends on it.
	second, err := fsys.CreateTemp(dir, ".basicopy-tmp-*")
	require.NoError(t, err)
	_, err = second.Write([]byte("replacement"))
	require.NoError(t, err)
	require.NoError(t, second.Close())
	require.NoError(t, fsys.Rename(second.Name(), final))
	got, err := os.ReadFile(final)
	require.NoError(t, err)
	assert.Equal(t, "replacement", string(got))

	require.NoError(t, fsys.Remove(final))
	_, err = fsys.Lstat(final)
	assert.True(t, errors.Is(err, os.ErrNotExist), "a missing remote file must report as not existing, got %v", err)
}

func TestRemoteMetadata(t *testing.T) {
	fsys := testFS(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	require.NoError(t, fsys.Chmod(path, 0o644))
	mt := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	require.NoError(t, fsys.Chtimes(path, mt, mt))

	fi, err := fsys.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
	assert.True(t, fi.ModTime().Equal(mt), "mtime must survive the round trip, got %v", fi.ModTime())

	uid, gid, ok := fsys.Owner(fi)
	require.True(t, ok, "SFTP attributes carry uid/gid")
	require.NoError(t, fsys.Chown(path, uid, gid))

	// Capabilities SFTP does not have must report unavailable rather than lie.
	_, _, ok = fsys.FileID(fi)
	assert.False(t, ok, "SFTP exposes no inode identity")
	_, ok = fsys.Dev(fi)
	assert.False(t, ok, "SFTP exposes no device number")
	assert.False(t, fsys.SameFile(fi, fi))
}

func TestRemoteDirectoriesAndLinks(t *testing.T) {
	fsys := testFS(t)
	dir := t.TempDir()

	nested := filepath.Join(dir, "a", "b", "c")
	require.NoError(t, fsys.MkdirAll(nested, 0o777))
	fi, err := fsys.Stat(nested)
	require.NoError(t, err)
	assert.True(t, fi.IsDir())

	require.NoError(t, os.WriteFile(filepath.Join(nested, "f.txt"), []byte("data"), 0o644))
	require.NoError(t, fsys.Symlink("f.txt", filepath.Join(nested, "link")))
	target, err := fsys.Readlink(filepath.Join(nested, "link"))
	require.NoError(t, err)
	assert.Equal(t, "f.txt", target)

	require.NoError(t, fsys.Link(filepath.Join(nested, "f.txt"), filepath.Join(nested, "hard")))
	hardInfo, err := fsys.Lstat(filepath.Join(nested, "hard"))
	require.NoError(t, err)
	assert.EqualValues(t, 4, hardInfo.Size())

	entries, err := fsys.ReadDir(nested)
	require.NoError(t, err)
	names := map[string]os.FileMode{}
	for _, e := range entries {
		names[e.Name()] = e.Mode()
	}
	require.Len(t, names, 3)
	assert.NotZero(t, names["link"]&os.ModeSymlink, "ReadDir must report the link itself, not its target")

	require.NoError(t, fsys.Mkdir(filepath.Join(dir, "explicit"), 0o700))
	fi, err = fsys.Lstat(filepath.Join(dir, "explicit"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	require.NoError(t, fsys.RemoveAll(filepath.Join(dir, "a")))
	_, err = fsys.Lstat(filepath.Join(dir, "a"))
	assert.Error(t, err)
}

func TestRemoteAvail(t *testing.T) {
	fsys := testFS(t)
	avail, ok := fsys.Avail(t.TempDir())
	require.True(t, ok, "the statvfs extension backs the pre-write space guard")
	assert.Positive(t, avail)

	_, ok = fsys.Avail("/definitely/not/here")
	assert.False(t, ok, "an unreadable path disables the guard rather than failing the run")
}

// TestConnectionPoolWidens covers the auto-scaled connection pool: growth is
// asynchronous, tops out at MaxConnections, and narrowing only changes how many
// connections carry work.
func TestConnectionPoolWidens(t *testing.T) {
	fsys := testFS(t)
	pool, ok := fsys.(Scalable)
	require.True(t, ok, "a remote FS must be scalable")

	width, open, frozen := pool.Pool()
	assert.Equal(t, 1, width)
	assert.Equal(t, 1, open)
	assert.False(t, frozen)

	pool.SetWidth(3)
	require.Eventually(t, func() bool {
		_, open, _ := pool.Pool()
		return open == 2
	}, 5*time.Second, 5*time.Millisecond, "one extra connection is dialed per request")

	pool.SetWidth(1)
	width, open, _ = pool.Pool()
	assert.Equal(t, 1, width, "narrowing stops routing work to the extra connection")
	assert.Equal(t, 2, open, "but does not close it mid-run")

	pool.SetWidth(MaxConnections + 5)
	require.Eventually(t, func() bool {
		_, open, _ := pool.Pool()
		return open == 3
	}, 5*time.Second, 5*time.Millisecond)

	// With more than one connection open, operations round-robin across them;
	// they must all reach the same filesystem.
	pool.SetWidth(2)
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f, err := fsys.CreateTemp(dir, "rr-*")
			assert.NoError(t, err)
			if err == nil {
				assert.NoError(t, f.Close())
			}
		}(i)
	}
	wg.Wait()
	entries, err := fsys.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 8)
}

// TestConnectionPoolFreezesOnDialFailure covers the case where the host cannot
// give a second connection without asking for a credential: the pool must stop
// trying rather than retry (and re-prompt) forever.
func TestConnectionPoolFreezesOnDialFailure(t *testing.T) {
	first, err := pipeTransport(Location{}, false)
	require.NoError(t, err)
	old := StartTransport
	StartTransport = func(Location, bool) (*Transport, error) { return first, nil }
	defer func() { StartTransport = old }()

	loc, err := Parse("somehost:/")
	require.NoError(t, err)
	fsys, err := Dial(loc)
	require.NoError(t, err)
	defer fsys.Close()

	dials := 0
	StartTransport = func(Location, bool) (*Transport, error) {
		dials++
		return nil, errors.New("Permission denied (publickey)")
	}

	pool := fsys.(Scalable)
	for i := 0; i < 5; i++ {
		pool.SetWidth(4)
		require.Eventually(t, func() bool {
			_, _, frozen := pool.Pool()
			return frozen
		}, 5*time.Second, 5*time.Millisecond)
	}
	assert.Equal(t, 1, dials, "a failed widen is never retried")

	width, open, _ := pool.Pool()
	assert.Equal(t, 1, width)
	assert.Equal(t, 1, open, "the run continues on the connection it already has")
}

func TestUnsupportedRecognisesSFTPStatus(t *testing.T) {
	fsys := testFS(t)
	dir := t.TempDir()
	f, err := fsys.CreateTemp(dir, "sync-*")
	require.NoError(t, err)
	defer f.Close()

	// The in-process server implements no fsync extension, which is exactly the
	// "server cannot do this" case a copy must tolerate rather than fail on.
	if err := f.Sync(); err != nil {
		assert.True(t, Unsupported(err), "an op-unsupported status must read as unsupported, got %v", err)
	}
	assert.False(t, Unsupported(nil))
	assert.False(t, Unsupported(errors.New("boom")))
	assert.True(t, Unsupported(errors.ErrUnsupported))
}

func TestRegistryReusesConnections(t *testing.T) {
	old := StartTransport
	StartTransport = pipeTransport
	defer func() { StartTransport = old }()

	reg := NewRegistry()
	defer reg.Close()

	a, pathA, err := reg.Resolve("pc:/srv/one")
	require.NoError(t, err)
	assert.Equal(t, "/srv/one", pathA)

	b, _, err := reg.Resolve("pc:/srv/two")
	require.NoError(t, err)
	assert.Same(t, a, b, "two paths on one host share a connection")

	local, pathLocal, err := reg.Resolve("/srv/local")
	require.NoError(t, err)
	assert.True(t, local.Local())
	assert.Equal(t, "/srv/local", pathLocal)

	other, _, err := reg.Resolve("otherpc:/srv")
	require.NoError(t, err)
	assert.NotSame(t, a, other, "a different host gets its own connection")
	assert.Len(t, reg.Remotes(), 2)

	_, _, err = reg.Resolve("@bad:/srv")
	assert.Error(t, err)

	require.NoError(t, reg.Close())
	assert.Empty(t, reg.Remotes())
}
