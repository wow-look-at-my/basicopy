//go:build !darwin && !linux && !netbsd

package fsx

func copyXattrs(src, dst string, nofollow bool) error { return nil }
