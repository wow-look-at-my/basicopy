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

// MaxConnections caps how far a remote FS may widen its connection pool. One
// connection with deep request pipelining saturates almost every link; the extra
// connections exist for the cases a single one cannot cover (single-core cipher
// throughput, the peer's serial sftp-server, one TCP flow on a lossy long-haul
// link), and past a handful they only add handshakes.
const MaxConnections = 8

// conn is one ssh process carrying one SFTP session.
type conn struct {
	client *sftp.Client
	close  func() error
}

// remoteFS is a host reached over SFTP. Paths are POSIX; capabilities the
// protocol does not carry (inode identity, st_dev, xattrs) report unavailable so
// the engine degrades instead of failing.
//
// It holds a pool of connections rather than one, because a single ssh
// connection is a single TCP flow encrypted by a single core and served by a
// single sftp-server process on the far end. The pool starts at one connection
// and is widened only by the engine's controller, when more parallel streams
// have stopped raising throughput — the same plateau signal that sizes the
// worker pool (see SetWidth).
type remoteFS struct {
	host   string // "[user@]host" for messages
	prefix string // "[user@]host:" path prefix for output
	dial   func() (*conn, error)

	mu      sync.Mutex
	conns   []*conn
	width   int  // connections currently carrying new work
	dialing bool // a widen is in flight
	frozen  bool // widening failed once; never retried

	rr atomic.Uint64

	homeOnce sync.Once
	home     string
}

// newRemoteFS wraps an established SFTP connection as an FS. host is the display
// name; dial (optional) opens further connections to the same host when the
// controller asks for more parallelism.
func newRemoteFS(first *conn, host string, dial func() (*conn, error)) FS {
	return &remoteFS{
		host:   host,
		prefix: host + ":",
		conns:  []*conn{first},
		width:  1,
		dial:   dial,
	}
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

// client picks the connection for the next operation, round-robin across the
// active width.
func (f *remoteFS) client() *sftp.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.width
	if n > len(f.conns) {
		n = len(f.conns)
	}
	if n <= 1 {
		return f.conns[0].client
	}
	return f.conns[int(f.rr.Add(1)-1)%n].client
}

// SetWidth asks for n connections to be carrying work. Growing dials one more
// connection in the background (the caller is never blocked on a handshake);
// shrinking simply stops routing new transfers to the extra connections, which
// then sit idle -- closing them mid-run would race in-flight file handles for no
// measurable gain.
func (f *remoteFS) SetWidth(n int) {
	if n < 1 {
		n = 1
	}
	if n > MaxConnections {
		n = MaxConnections
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if n <= len(f.conns) {
		f.width = n
		return
	}
	f.width = len(f.conns)
	if f.frozen || f.dialing || f.dial == nil {
		return
	}
	f.dialing = true
	go f.widen()
}

// widen opens one additional connection. A failure freezes the pool at its
// current size: the usual cause is that the host needs an interactive
// credential, which the extra connections deliberately refuse to prompt for.
func (f *remoteFS) widen() {
	c, err := f.dial()

	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialing = false
	if err != nil {
		f.frozen = true
		return
	}
	f.conns = append(f.conns, c)
	f.width = len(f.conns)
}

// Pool reports the connection pool's state: how many connections carry work, how
// many are open, and whether widening has been given up on.
func (f *remoteFS) Pool() (width, open int, frozen bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.width, len(f.conns), f.frozen
}

func (f *remoteFS) Close() error {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()

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
		if wd, err := f.client().Getwd(); err == nil {
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

func (f *remoteFS) Lstat(name string) (os.FileInfo, error)      { return f.client().Lstat(name) }
func (f *remoteFS) Stat(name string) (os.FileInfo, error)       { return f.client().Stat(name) }
func (f *remoteFS) ReadDir(name string) ([]os.FileInfo, error)   { return f.client().ReadDir(name) }
func (f *remoteFS) Readlink(name string) (string, error)         { return f.client().ReadLink(name) }
func (f *remoteFS) Symlink(target, name string) error            { return f.client().Symlink(target, name) }
func (f *remoteFS) Link(target, name string) error               { return f.client().Link(target, name) }
func (f *remoteFS) Remove(name string) error                     { return f.client().Remove(name) }
func (f *remoteFS) RemoveAll(name string) error                  { return f.client().RemoveAll(name) }
func (f *remoteFS) Chmod(name string, mode os.FileMode) error    { return f.client().Chmod(name, mode) }
func (f *remoteFS) Chown(name string, uid, gid int) error        { return f.client().Chown(name, uid, gid) }

func (f *remoteFS) Chtimes(name string, atime, mtime time.Time) error {
	return f.client().Chtimes(name, atime, mtime)
}

// Mkdir lets the server apply its own umask (as a local mkdir does) unless an
// explicit non-default mode is asked for; directory metadata is applied later by
// the copy engine anyway.
func (f *remoteFS) Mkdir(name string, perm os.FileMode) error {
	c := f.client()
	if err := c.Mkdir(name); err != nil {
		return err
	}
	if perm.Perm() != 0o777 {
		return c.Chmod(name, perm)
	}
	return nil
}

func (f *remoteFS) MkdirAll(name string, perm os.FileMode) error {
	return f.client().MkdirAll(name)
}

// Rename replaces an existing destination, which plain SSH_FXP_RENAME refuses.
// OpenSSH's posix-rename extension does it atomically; without the extension we
// fall back to unlink-then-rename (a hair less atomic, still crash-safe in the
// sense that no partial file is ever published at the final path).
func (f *remoteFS) Rename(from, to string) error {
	c := f.client()
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
}

func (f *remoteFS) Open(name string) (File, error) {
	file, err := f.client().Open(name)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// CreateTemp mirrors os.CreateTemp: an exclusive create under dir with the "*"
// in pattern replaced by random characters, retried on collision.
func (f *remoteFS) CreateTemp(dir, pattern string) (File, error) {
	prefix, suffix := pattern, ""
	if i := strings.LastIndex(pattern, "*"); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	c := f.client()
	var buf [8]byte
	for attempt := 0; attempt < 100; attempt++ {
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, err
		}
		name := path.Join(dir, prefix+hex.EncodeToString(buf[:])+suffix)
		file, err := c.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL)
		if err == nil {
			return file, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("create temp file in %s: too many collisions", f.Display(dir))
}

func (f *remoteFS) Avail(dir string) (int64, bool) {
	st, err := f.client().StatVFS(dir)
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

// FileID reports no identity: SFTP carries no inode number or link count, so
// hardlinked sources on a remote host are copied as independent files.
func (f *remoteFS) FileID(fi os.FileInfo) (string, uint64, bool) { return "", 0, false }

// Dev reports no device number, so --one-file-system cannot detect a remote
// mount point.
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
