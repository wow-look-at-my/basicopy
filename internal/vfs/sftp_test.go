package vfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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
	// A remote read can return data and io.EOF in the same call.
	n, err := in.Read(buf)
	if err != nil {
		require.ErrorIs(t, err, io.EOF)
	}
	assert.Equal(t, "hello remote", string(buf[:n]))
	require.NoError(t, in.Close())

	// Rename must replace an existing destination: the atomic-publish step depends on it.
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

// TestPoolGrowsWithConcurrentTransfers is the property the copy depends on:
// another transfer must not be made to queue behind the earliest on a single
// connection, because the far side would run them in sequence.
func TestPoolGrowsWithConcurrentTransfers(t *testing.T) {
	fsys := testFS(t)
	pooled, ok := fsys.(Pooled)
	require.True(t, ok, "a remote FS must report its pool")

	open, frozen := pooled.Pool()
	assert.Equal(t, 1, open, "a copy starts on one connection")
	assert.False(t, frozen)

	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	// Hold a single transfer open; the next must get its own connection, not share.
	first, err := fsys.Open(path)
	require.NoError(t, err)
	rf := fsys.(*remoteFS)
	rf.conns[0].inflight.Add(1)

	second, err := fsys.Open(path)
	require.NoError(t, err)
	open, _ = pooled.Pool()
	assert.Equal(t, 2, open, "a concurrent transfer opens a connection")
	assert.NotSame(t, second.(*remoteFile).c, first.(*remoteFile).c,
		"and runs on it, not behind the first transfer")

	rf.conns[0].inflight.Add(-1)
	require.NoError(t, first.Close())
	require.NoError(t, second.Close())

	// With both transfers finished, the next a single reuses a free connection.
	third, err := fsys.Open(path)
	require.NoError(t, err)
	open, _ = pooled.Pool()
	assert.Equal(t, 2, open, "an idle pool is reused, not grown")
	require.NoError(t, third.Close())
}

// TestPoolStopsAtMaxConnections: transfers share connections rather than wait
// forever a single time the ceiling is reached.
func TestPoolStopsAtMaxConnections(t *testing.T) {
	fsys := testFS(t)
	rf := fsys.(*remoteFS)
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	var files []File
	for i := 0; i < MaxConnections+4; i++ {
		f, err := fsys.Open(path)
		require.NoError(t, err)
		files = append(files, f)
		rf.mu.Lock()
		for _, c := range rf.conns {
			c.inflight.Store(1)
		}
		rf.mu.Unlock()
	}
	open, _ := fsys.(Pooled).Pool()
	assert.Equal(t, MaxConnections, open, "the pool stops at the ceiling instead of dialing forever")

	rf.mu.Lock()
	for _, c := range rf.conns {
		c.inflight.Store(0)
	}
	rf.mu.Unlock()
	for _, f := range files {
		require.NoError(t, f.Close())
	}
}

// TestFirstConnectionStaysClearForMetadata: a single time the pool has grown,
// directory listings and stats must not queue behind a file that takes a
// minute to read, or the walk stops feeding the workers.
func TestFirstConnectionStaysClearForMetadata(t *testing.T) {
	fsys := testFS(t)
	rf := fsys.(*remoteFS)
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	first, err := fsys.Open(path)
	require.NoError(t, err)
	rf.conns[0].inflight.Add(1)
	second, err := fsys.Open(path)
	require.NoError(t, err)
	require.Len(t, rf.conns, 2)

	// The transfer that arrived after growth went to the new connection.
	assert.Same(t, rf.conns[1], second.(*remoteFile).c)
	rf.conns[0].inflight.Add(-1)
	require.NoError(t, first.Close())
	assert.Same(t, rf.conns[0], rf.pick(), "metadata prefers the connection carrying no transfer")

	require.NoError(t, second.Close())
}

func TestUnsupportedRecognisesSFTPStatus(t *testing.T) {
	fsys := testFS(t)
	dir := t.TempDir()
	f, err := fsys.CreateTemp(dir, "sync-*")
	require.NoError(t, err)
	defer f.Close()

	// The in-process server implements no fsync extension: a copy must tolerate this.
	if err := f.Sync(); err != nil {
		assert.True(t, Unsupported(err), "an op-unsupported status must read as unsupported, got %v", err)
	}
	assert.False(t, Unsupported(nil))
	assert.False(t, Unsupported(errors.New("boom")))
	assert.True(t, Unsupported(errors.ErrUnsupported))
}

func TestRegistryReusesConnections(t *testing.T) {
	t.Serial()
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
