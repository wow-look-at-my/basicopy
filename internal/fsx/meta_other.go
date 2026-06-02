//go:build !unix

package fsx

import "os"

// preserveOwner is a no-op on platforms without Unix ownership semantics.
func preserveOwner(dst string, info os.FileInfo) error { return nil }
