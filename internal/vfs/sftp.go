package vfs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
)

// remoteFS is a host reached over SFTP. Paths are POSIX; capabilities the protocol
// lacks (inode identity, st_dev, xattrs) report unavailable.
//
// Each concurrent transfer gets its own connection, up to MaxConnections: a
// server answers a single request at a time, so transfers sharing a connection
// queue instead of running in parallel. Pool width tracks the worker controller's W.
type remoteFS struct {
	host   string // "[user@]host" for messages
	prefix string // "[user@]host:" path prefix for output
	dial   func() (*conn, error)

	mu      sync.Mutex
	free    *sync.Cond // woken when a transfer ends or a connection arrives
	conns   []*conn
	dialing bool // a dial is in flight
	frozen  bool // dialing failed a single time;
	closed  bool // the FS has been closed; no more dialing

	rr atomic.Uint64

	homeOnce sync.Once
	home     string
}

// newRemoteFS wraps an established SFTP connection as an FS. host is the display
// name; dial (optional) opens further connections to the same host as
// concurrent transfers need them.
func newRemoteFS(first *conn, host string, dial func() (*conn, error)) FS {
	f := &remoteFS{
		host:   host,
		prefix: host + ":",
		conns:  []*conn{first},
		dial:   dial,
	}
	f.free = sync.NewCond(&f.mu)
	return f
}

func (f *remoteFS) Local() bool                { return false }
func (f *remoteFS) Host() string               { return f.host }
func (f *remoteFS) Display(p string) string    { return f.prefix + p }
func (f *remoteFS) Separator() string          { return "/" }
func (f *remoteFS) Join(elem ...string) string { return path.Join(elem...) }
func (f *remoteFS) Clean(p string) string      { return path.Clean(p) }
func (f *remoteFS) Dir(p string) string        { return path.Dir(p) }
func (f *remoteFS) Base(p string) string       { return path.Base(p) }
func (f *remoteFS) IsAbs(p string) bool        { return path.IsAbs(p) }

// remoteFile is bound to the connection it opened on; a slow read shows as busy,
// and close frees the connection's transfer slot.
type remoteFile struct {
	*sftp.File
	fs     *remoteFS
	c      *conn
	closed bool
}

func (r *remoteFile) Read(p []byte) (int, error) {
	r.c.begin()
	n, err := r.File.Read(p)
	r.c.finish()
	return n, err
}

func (r *remoteFile) Write(p []byte) (int, error) {
	r.c.begin()
	n, err := r.File.Write(p)
	r.c.finish()
	return n, err
}

func (r *remoteFile) Close() error {
	err := r.File.Close()
	if !r.closed {
		r.closed = true
		r.fs.release(r.c)
	}
	return err
}

func (f *remoteFS) Close() error {
	// Keep conns intact: a held transfer gets a truthful closed-client error, not a race.
	f.mu.Lock()
	f.closed = true
	conns := f.conns
	f.mu.Unlock()
	f.free.Broadcast()

	var firstErr error
	for _, c := range conns {
		if err := c.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if c.close != nil {
			if err := c.close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Abs resolves a relative remote path against the login directory, which is
// what "host:notes" (no leading slash) means to scp and rsync alike.
func (f *remoteFS) Abs(p string) (string, error) {
	if path.IsAbs(p) {
		return path.Clean(p), nil
	}
	f.homeOnce.Do(func() {
		if wd, err := run(f, func(c *sftp.Client) (string, error) { return c.Getwd() }); err == nil {
			f.home = wd
		}
	})
	if f.home == "" {
		return "", fmt.Errorf("cannot resolve relative path %q on %s: remote working directory unknown", p, f.host)
	}
	return path.Join(f.home, p), nil
}

func (f *remoteFS) Rel(basepath, target string) (string, error) {
	// path has no Rel; reuse the lexical algorithm on cleaned POSIX paths.
	return relPosix(path.Clean(basepath), path.Clean(target))
}

func (f *remoteFS) Match(pattern, name string) (bool, error) { return path.Match(pattern, name) }

func (f *remoteFS) Lstat(name string) (os.FileInfo, error) {
	return run(f, func(c *sftp.Client) (os.FileInfo, error) { return c.Lstat(name) })
}

func (f *remoteFS) Stat(name string) (os.FileInfo, error) {
	return run(f, func(c *sftp.Client) (os.FileInfo, error) { return c.Stat(name) })
}

func (f *remoteFS) ReadDir(name string) ([]os.FileInfo, error) {
	return run(f, func(c *sftp.Client) ([]os.FileInfo, error) { return c.ReadDir(name) })
}

func (f *remoteFS) Readlink(name string) (string, error) {
	return run(f, func(c *sftp.Client) (string, error) { return c.ReadLink(name) })
}

func (f *remoteFS) Symlink(target, name string) error {
	return runErr(f, func(c *sftp.Client) error { return c.Symlink(target, name) })
}

func (f *remoteFS) Link(target, name string) error {
	return runErr(f, func(c *sftp.Client) error { return c.Link(target, name) })
}

func (f *remoteFS) Remove(name string) error {
	return runErr(f, func(c *sftp.Client) error { return c.Remove(name) })
}

func (f *remoteFS) RemoveAll(name string) error {
	return runErr(f, func(c *sftp.Client) error { return c.RemoveAll(name) })
}

func (f *remoteFS) Chmod(name string, mode os.FileMode) error {
	return runErr(f, func(c *sftp.Client) error { return c.Chmod(name, mode) })
}

func (f *remoteFS) Chown(name string, uid, gid int) error {
	return runErr(f, func(c *sftp.Client) error { return c.Chown(name, uid, gid) })
}

func (f *remoteFS) Chtimes(name string, atime, mtime time.Time) error {
	return runErr(f, func(c *sftp.Client) error { return c.Chtimes(name, atime, mtime) })
}

// Mkdir lets the server apply its own umask (as a local mkdir does) unless an
// explicit non-default mode is asked for; directory metadata is applied later by
// the copy engine anyway.
func (f *remoteFS) Mkdir(name string, perm os.FileMode) error {
	return runErr(f, func(c *sftp.Client) error {
		if err := c.Mkdir(name); err != nil {
			return err
		}
		if perm.Perm() != 0o777 {
			return c.Chmod(name, perm)
		}
		return nil
	})
}

func (f *remoteFS) MkdirAll(name string, perm os.FileMode) error {
	return runErr(f, func(c *sftp.Client) error { return c.MkdirAll(name) })
}

// Rename replaces an existing destination, which plain SSH_FXP_RENAME refuses.
// OpenSSH's posix-rename extension does it atomically; without the extension we
// fall back to unlink-then-rename (a hair less atomic, still crash-safe in the
// sense that no partial file is ever published at the final path).
func (f *remoteFS) Rename(from, to string) error {
	return runErr(f, func(c *sftp.Client) error {
		if _, ok := c.HasExtension("posix-rename@openssh.com"); ok {
			return c.PosixRename(from, to)
		}
		if err := c.Rename(from, to); err == nil {
			return nil
		}
		if err := c.Remove(to); err != nil && !os.IsNotExist(err) {
			return err
		}
		return c.Rename(from, to)
	})
}

// Open reserves a connection for this file and reads it there until it is
// closed.
func (f *remoteFS) Open(name string) (File, error) {
	c := f.acquire()
	c.begin()
	file, err := c.client.Open(name)
	c.finish()
	if err != nil {
		f.release(c)
		return nil, err
	}
	return &remoteFile{File: file, fs: f, c: c}, nil
}

// CreateTemp mirrors os.CreateTemp: an exclusive create under dir with the "*"
// in pattern replaced by random characters, retried on collision.
func (f *remoteFS) CreateTemp(dir, pattern string) (File, error) {
	prefix, suffix := pattern, ""
	if i := strings.LastIndex(pattern, "*"); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	c := f.acquire()
	var buf [8]byte
	for attempt := 0; attempt < 100; attempt++ {
		if _, err := rand.Read(buf[:]); err != nil {
			f.release(c)
			return nil, err
		}
		name := path.Join(dir, prefix+hex.EncodeToString(buf[:])+suffix)
		c.begin()
		file, err := c.client.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL)
		c.finish()
		if err == nil {
			return &remoteFile{File: file, fs: f, c: c}, nil
		}
		if !os.IsExist(err) {
			f.release(c)
			return nil, err
		}
	}
	f.release(c)
	return nil, fmt.Errorf("create temp file in %s: too many collisions", f.Display(dir))
}

func (f *remoteFS) Avail(dir string) (int64, bool) {
	st, err := run(f, func(c *sftp.Client) (*sftp.StatVFS, error) { return c.StatVFS(dir) })
	if err != nil {
		return 0, false
	}
	unit := st.Frsize
	if unit == 0 {
		unit = st.Bsize
	}
	return int64(st.Bavail * unit), true
}

func (f *remoteFS) Owner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*sftp.FileStat)
	if !ok {
		return 0, 0, false
	}
	return int(st.UID), int(st.GID), true
}

// FileID reports no identity: SFTP has no inode number, so remote hardlinks copy as independent files.
func (f *remoteFS) FileID(fi os.FileInfo) (string, uint64, bool) { return "", 0, false }

// Dev reports no device number, so --a single file-system can't detect a remote mount point.
func (f *remoteFS) Dev(fi os.FileInfo) (uint64, bool) { return 0, false }

func (f *remoteFS) SameFile(a, b os.FileInfo) bool { return false }

// relPosix is filepath.Rel's lexical algorithm for POSIX paths (both arguments
// already cleaned, and either both absolute or both relative).
func relPosix(base, target string) (string, error) {
	if base == target {
		return ".", nil
	}
	if path.IsAbs(base) != path.IsAbs(target) {
		return "", fmt.Errorf("Rel: can't make %s relative to %s", target, base)
	}
	baseParts := splitParts(base)
	targetParts := splitParts(target)

	i := 0
	for i < len(baseParts) && i < len(targetParts) && baseParts[i] == targetParts[i] {
		i++
	}
	var out []string
	for range baseParts[i:] {
		out = append(out, "..")
	}
	out = append(out, targetParts[i:]...)
	if len(out) == 0 {
		return ".", nil
	}
	return path.Join(out...), nil
}

func splitParts(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}
