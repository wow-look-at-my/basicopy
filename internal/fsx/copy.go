// Package fsx holds the low-level filesystem copy primitives: content copying
// (with crash-safe temp-file + atomic rename), sparse handling, preallocation,
// and metadata preservation. Fast in-kernel paths (copy_file_range, clonefile,
// CopyFileEx) are layered on in build-tagged files; copy.go is the portable core.
package fsx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultBufSize is the portable fallback buffer used when no device-adaptive
// size has been chosen.
const DefaultBufSize = 1 << 20 // 1 MiB

// CopyOptions controls a single file copy.
type CopyOptions struct {
	Preserve bool // preserve mode/times/owner (and later xattr/ACL).
	Fsync    bool // fsync the temp file before the atomic rename.
	BufSize  int  // copy buffer size; <= 0 uses DefaultBufSize.
}

// CopyFile copies the regular file at src to dst, crash-safely: the bytes are
// written to a temporary file in dst's directory, metadata is applied, and the
// temp file is atomically renamed into place. An interrupted copy therefore never
// leaves a partial file at the final path. info is src's Lstat result and is used
// for metadata preservation. It returns the number of bytes copied.
func CopyFile(src, dst string, info os.FileInfo, o CopyOptions) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	perm := os.FileMode(0o644)
	if info != nil {
		perm = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".basicopy-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temp for %s: %w", dst, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	bufSize := o.BufSize
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	buf := make([]byte, bufSize)
	n, err := io.CopyBuffer(tmp, in, buf)
	if err != nil {
		return n, fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}

	if err := tmp.Chmod(perm); err != nil {
		return n, fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if o.Preserve && info != nil {
		// Owner/xattr applied to the temp path so the rename publishes a fully
		// formed file; times are applied after close (below) for the same reason.
		if err := preserveOwner(tmpName, info); err != nil {
			return n, err
		}
	}
	if o.Fsync {
		if err := tmp.Sync(); err != nil {
			return n, fmt.Errorf("fsync %s: %w", tmpName, err)
		}
	}
	if err := tmp.Close(); err != nil {
		return n, fmt.Errorf("close %s: %w", tmpName, err)
	}

	if o.Preserve && info != nil {
		if err := preserveTimes(tmpName, info); err != nil {
			return n, err
		}
	}

	if err := os.Rename(tmpName, dst); err != nil {
		return n, fmt.Errorf("rename %s -> %s: %w", tmpName, dst, err)
	}
	committed = true
	return n, nil
}
