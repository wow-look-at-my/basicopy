//go:build darwin || linux || netbsd || cosmo

// xattrSys is the extended-attribute syscall seam; tests swap it to inject errnos.
// cosmo is in the tag because the shipped APE runs that personality off Linux,
// and its syscall layer now serves the xattr calls. Without it the copier
// compiled this pass out there and dropped every attribute without a word.

package fsx

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// xattrSys is the extended-attribute syscall seam; tests swap it to inject errnos.
var xattrSys = struct {
	list func(path string, dest []byte, nofollow bool) (int, error)
	get  func(path, name string, dest []byte, nofollow bool) (int, error)
	set  func(path, name string, value []byte, nofollow bool) error
}{listXattrBytes, getXattrBytes, setXattr}

func copyXattrs(src, dst string, nofollow bool) error {
	names, err := listXattrs(src, nofollow)
	if err != nil {
		if ignorableXattrErr(err) {
			xattrSkipped(src, "", err)
			return nil
		}
		return fmt.Errorf("list xattrs on %s: %w", src, err)
	}
	for _, name := range names {
		value, err := getXattr(src, name, nofollow)
		if err != nil {
			if ignorableXattrErr(err) {
				xattrSkipped(src, name, err)
				continue
			}
			return fmt.Errorf("get xattr %s on %s: %w", name, src, err)
		}
		if err := xattrSys.set(dst, name, value, nofollow); err != nil {
			if ignorableXattrErr(err) {
				xattrSkipped(dst, name, err)
				continue
			}
			return fmt.Errorf("set xattr %s on %s: %w", name, dst, err)
		}
	}
	return nil
}

// xattrFirstBuf holds almost every name list and value, so one call usually reads it.
const xattrFirstBuf = 4096

// readXattr calls read with a fixed buffer first, and asks for the size only on ERANGE.
func readXattr(read func(dest []byte) (int, error)) ([]byte, error) {
	buf := make([]byte, xattrFirstBuf)
	n, err := read(buf)
	if err == nil {
		return buf[:n], nil
	}
	if !errors.Is(err, unix.ERANGE) {
		return nil, err
	}
	size, err := read(nil)
	if err != nil {
		return nil, err
	}
	buf = make([]byte, size)
	n, err = read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func listXattrs(path string, nofollow bool) ([]string, error) {
	list, err := readXattr(func(dest []byte) (int, error) { return xattrSys.list(path, dest, nofollow) })
	if err != nil || len(list) == 0 {
		return nil, err
	}
	parts := bytes.Split(list, []byte{0})
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			names = append(names, string(p))
		}
	}
	return names, nil
}

func listXattrBytes(path string, dest []byte, nofollow bool) (int, error) {
	if nofollow {
		return unix.Llistxattr(path, dest)
	}
	return unix.Listxattr(path, dest)
}

func getXattr(path, name string, nofollow bool) ([]byte, error) {
	return readXattr(func(dest []byte) (int, error) { return xattrSys.get(path, name, dest, nofollow) })
}

func getXattrBytes(path, name string, dest []byte, nofollow bool) (int, error) {
	if nofollow {
		return unix.Lgetxattr(path, name, dest)
	}
	return unix.Getxattr(path, name, dest)
}

func setXattr(path, name string, value []byte, nofollow bool) error {
	if nofollow {
		return unix.Lsetxattr(path, name, value, 0)
	}
	return unix.Setxattr(path, name, value, 0)
}

// ignorableXattrErr: ENOTSUP, EOPNOTSUPP, ENOSYS (OpenZFS on macOS, every call on NT) mean no xattrs.
func ignorableXattrErr(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, unix.ENODATA) ||
		os.IsPermission(err)
}
