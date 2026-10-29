//go:build darwin || linux || netbsd

// xattrSys is the extended-attribute syscall seam; tests swap it to inject errnos.

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
			return nil
		}
		return fmt.Errorf("list xattrs on %s: %w", src, err)
	}
	for _, name := range names {
		value, err := getXattr(src, name, nofollow)
		if err != nil {
			if ignorableXattrErr(err) {
				continue
			}
			return fmt.Errorf("get xattr %s on %s: %w", name, src, err)
		}
		if err := xattrSys.set(dst, name, value, nofollow); err != nil {
			if ignorableXattrErr(err) {
				continue
			}
			return fmt.Errorf("set xattr %s on %s: %w", name, dst, err)
		}
	}
	return nil
}

func listXattrs(path string, nofollow bool) ([]string, error) {
	size, err := xattrSys.list(path, nil, nofollow)
	if err != nil || size == 0 {
		return nil, err
	}
	buf := make([]byte, size)
	n, err := xattrSys.list(path, buf, nofollow)
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(buf[:n], []byte{0})
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
	size, err := xattrSys.get(path, name, nil, nofollow)
	if err != nil {
		return nil, err
	}
	value := make([]byte, size)
	_, err = xattrSys.get(path, name, value, nofollow)
	return value, err
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

// ignorableXattrErr: ENOTSUP, EOPNOTSUPP, ENOSYS (OpenZFS on macOS) mean no xattrs.
func ignorableXattrErr(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, unix.ENODATA) ||
		os.IsPermission(err)
}
