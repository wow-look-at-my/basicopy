// Local passes every call straight through to the os package.

package vfs

import (
	"os"
	"path/filepath"
	"time"
)

// Local passes every call straight through to the os package.
var Local FS = localFS{}

type localFS struct{}

func (localFS) Local() bool                  { return true }
func (localFS) Host() string                 { return "" }
func (localFS) Display(path string) string   { return path }
func (localFS) Close() error                 { return nil }
func (localFS) Separator() string            { return string(filepath.Separator) }
func (localFS) Join(elem ...string) string   { return filepath.Join(elem...) }
func (localFS) Clean(p string) string        { return filepath.Clean(p) }
func (localFS) Dir(p string) string          { return filepath.Dir(p) }
func (localFS) Base(p string) string         { return filepath.Base(p) }
func (localFS) IsAbs(p string) bool          { return filepath.IsAbs(p) }
func (localFS) Abs(p string) (string, error) { return filepath.Abs(p) }

func (localFS) Rel(basepath, target string) (string, error) { return filepath.Rel(basepath, target) }
func (localFS) Match(pattern, name string) (bool, error)    { return filepath.Match(pattern, name) }

func (localFS) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }
func (localFS) Stat(name string) (os.FileInfo, error)  { return os.Stat(name) }
func (localFS) Readlink(name string) (string, error)   { return os.Readlink(name) }
func (localFS) Symlink(target, name string) error      { return os.Symlink(target, name) }
func (localFS) Link(target, name string) error         { return os.Link(target, name) }
func (localFS) Remove(name string) error               { return os.Remove(name) }
func (localFS) RemoveAll(name string) error            { return os.RemoveAll(name) }
func (localFS) Rename(from, to string) error           { return os.Rename(from, to) }

func (localFS) Mkdir(name string, perm os.FileMode) error    { return os.Mkdir(name, perm) }
func (localFS) MkdirAll(name string, perm os.FileMode) error { return os.MkdirAll(name, perm) }
func (localFS) Chmod(name string, mode os.FileMode) error    { return os.Chmod(name, mode) }

// Chown does not follow a symlink: a recreated link keeps its own owner, not the target's.
func (localFS) Chown(name string, uid, gid int) error { return os.Lchown(name, uid, gid) }

func (localFS) Chtimes(name string, atime, mtime time.Time) error {
	return os.Chtimes(name, atime, mtime)
}

func (localFS) ReadDir(name string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue // vanished between readdir and lstat; treat as absent
		}
		infos = append(infos, fi)
	}
	return infos, nil
}

func (localFS) Open(name string) (File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (localFS) CreateTemp(dir, pattern string) (File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (localFS) SameFile(a, b os.FileInfo) bool { return os.SameFile(a, b) }

func (localFS) Avail(dir string) (int64, bool) { return statfsAvail(dir) }

func (localFS) Owner(fi os.FileInfo) (uid, gid int, ok bool) { return localOwner(fi) }

func (localFS) FileID(fi os.FileInfo) (key string, nlink uint64, ok bool) { return localFileID(fi) }

func (localFS) Dev(fi os.FileInfo) (uint64, bool) { return localDev(fi) }
