//go:build darwin || linux || netbsd

package fsx

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

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
		if err := setXattr(dst, name, value, nofollow); err != nil {
			if ignorableXattrErr(err) {
				continue
			}
			return fmt.Errorf("set xattr %s on %s: %w", name, dst, err)
		}
	}
	return nil
}

func listXattrs(path string, nofollow bool) ([]string, error) {
	size, err := listXattrBytes(path, nil, nofollow)
	if err != nil || size == 0 {
		return nil, err
	}
	buf := make([]byte, size)
	n, err := listXattrBytes(path, buf, nofollow)
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
	size, err := getXattrBytes(path, name, nil, nofollow)
	if err != nil {
		return nil, err
	}
	value := make([]byte, size)
	_, err = getXattrBytes(path, name, value, nofollow)
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

func ignorableXattrErr(err error) bool {
	return err == unix.ENOTSUP ||
		err == unix.EOPNOTSUPP ||
		err == unix.ENODATA ||
		os.IsPermission(err)
}
