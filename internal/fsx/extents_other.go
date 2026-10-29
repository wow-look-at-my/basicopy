//go:build !linux

// sameInode is never reached here: only the Linux copy path records extents.

package fsx

import "os"

// sameInode is never reached here: only the Linux copy path records extents.
func sameInode(*os.File, uint64, uint64) bool { return false }
