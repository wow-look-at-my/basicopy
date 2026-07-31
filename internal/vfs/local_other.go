//go:build !unix

package vfs

import "os"

// statfsAvail is unavailable off Unix, so the pre-write space guard is disabled
// there (the ENOSPC abort still stops a full destination, just reactively).
func statfsAvail(path string) (int64, bool) { return 0, false }

// localOwner reports no ownership off Unix, so owner drift is never detected.
func localOwner(fi os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// localFileID reports no inode identity off Unix, so hardlink preservation is a
// no-op there (files are simply copied).
func localFileID(fi os.FileInfo) (key string, nlink uint64, ok bool) { return "", 0, false }

func localDev(fi os.FileInfo) (uint64, bool) { return 0, false }
