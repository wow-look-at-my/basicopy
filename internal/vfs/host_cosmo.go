//go:build cosmo

// host_cosmo.go holds hostIsWindows.

package vfs

import "runtime"

// hostIsWindows reports whether this binary runs on Windows now.
func hostIsWindows() bool { return runtime.CosmoHostOS() == "windows" }
