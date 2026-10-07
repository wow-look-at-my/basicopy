//go:build !unix

package vfs

import "os"

// Off Unix the pre-write space guard is disabled; a full destination still aborts on ENOSPC.
func statfsAvail(path string) (int64, bool) { return 0, false }

// localOwner reports no ownership off Unix, so owner drift is never detected.
func localOwner(fi os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// Off Unix, hardlink preservation is a no-op: files are copied.
func localFileID(fi os.FileInfo) (key string, nlink uint64, ok bool) { return "", 0, false }

func localDev(fi os.FileInfo) (uint64, bool) { return 0, false }
