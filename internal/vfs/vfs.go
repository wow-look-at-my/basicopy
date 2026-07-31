// Package vfs is the filesystem seam that lets the engine treat a remote host
// reached over SSH exactly like the local disk. A local FS is a thin shell over
// the os package (same syscalls, same path rules as before); a remote FS speaks
// SFTP over an ssh subprocess, so every OpenSSH feature -- ~/.ssh/config host
// aliases, agent and key auth, ProxyJump, known_hosts -- applies without
// basicopy reimplementing any of it.
//
// Remote paths are always POSIX, so path manipulation lives on the FS rather
// than in the caller: use fs.Join/Dir/Base, never filepath.*, on a path that
// might be remote.
package vfs

import (
	"errors"
	"io"
	"os"
	"time"

	"github.com/pkg/sftp"
)

// File is the subset of *os.File the copy primitives need, so an SFTP file
// handle can stand in for a local one.
type File interface {
	io.ReadWriteCloser
	io.Seeker
	Name() string
	Truncate(size int64) error
	Sync() error
	Chmod(mode os.FileMode) error
}

// FS is one side of a copy: the local machine or one remote host.
//
// Capability queries (Owner, FileID, Dev, Avail) return ok=false rather than an
// error when the underlying filesystem or protocol cannot answer -- SFTP has no
// inode numbers, for instance -- so callers degrade instead of failing.
type FS interface {
	// Local reports whether this is the machine basicopy runs on.
	Local() bool
	// Host is the "[user@]host" the FS is connected to, empty when local.
	Host() string
	// Display renders a path for output, prefixed with "host:" when remote.
	Display(path string) string
	// Close releases the connection (a no-op for the local FS).
	Close() error

	// Path helpers: OS rules locally, POSIX remotely.
	Join(elem ...string) string
	Clean(p string) string
	Dir(p string) string
	Base(p string) string
	IsAbs(p string) bool
	Abs(p string) (string, error)
	Rel(basepath, target string) (string, error)
	Match(pattern, name string) (bool, error)
	Separator() string

	// Metadata and directory operations.
	Lstat(name string) (os.FileInfo, error)
	Stat(name string) (os.FileInfo, error)
	ReadDir(name string) ([]os.FileInfo, error)
	Readlink(name string) (string, error)
	Symlink(target, name string) error
	Link(target, name string) error
	Mkdir(name string, perm os.FileMode) error
	MkdirAll(name string, perm os.FileMode) error
	Remove(name string) error
	RemoveAll(name string) error
	// Rename moves from onto to, replacing an existing to.
	Rename(from, to string) error
	Chmod(name string, mode os.FileMode) error
	Chown(name string, uid, gid int) error
	Chtimes(name string, atime, mtime time.Time) error

	// Content.
	Open(name string) (File, error)
	CreateTemp(dir, pattern string) (File, error)

	// Capability-dependent extras.
	Avail(dir string) (int64, bool)                            // free bytes for the space guard
	Owner(fi os.FileInfo) (uid, gid int, ok bool)              // Unix ownership
	FileID(fi os.FileInfo) (key string, nlink uint64, ok bool) // inode identity (hardlinks)
	Dev(fi os.FileInfo) (dev uint64, ok bool)                  // st_dev (--one-file-system)
	SameFile(a, b os.FileInfo) bool
}

// Unsupported reports whether err means "this filesystem cannot do that" rather
// than "that failed". The standard library maps ENOSYS/ENOTSUP/EOPNOTSUPP to
// errors.ErrUnsupported; an SFTP server answers the same thing with an
// op-unsupported status, typically for an optional extension such as fsync or
// hardlink. Callers use it to skip an operation instead of failing a copy whose
// data is already safely written.
func Unsupported(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errors.ErrUnsupported) {
		return true
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return status.FxCode() == sftp.ErrSSHFxOpUnsupported
	}
	return false
}

// Pooled is implemented by an FS backed by a self-sizing connection pool --
// today, the SSH connections behind a remote host. It exists so a run can
// report what the pool did, not so anything can steer it: the pool follows the
// transfers asked of it, and how many of those run at once is already a
// measured decision (see control.Controller).
type Pooled interface {
	// Pool reports how many connections are open and whether opening more has
	// been abandoned (a host that needs an interactive credential cannot be
	// asked for one twice mid-run).
	Pool() (open int, frozen bool)
}

// SameHost reports whether two FS values address the same filesystem namespace,
// i.e. whether a path on one is meaningful on the other. Two connections to the
// same host count as the same host.
func SameHost(a, b FS) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Local() != b.Local() {
		return false
	}
	return a.Host() == b.Host()
}
