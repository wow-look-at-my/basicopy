//go:build !darwin && !linux && !netbsd && !cosmo

package fsx

func copyXattrs(src, dst string, nofollow bool) error { return nil }
