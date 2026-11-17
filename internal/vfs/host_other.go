//go:build !cosmo

package vfs

import "runtime"

func hostIsWindows() bool { return runtime.GOOS == "windows" }
