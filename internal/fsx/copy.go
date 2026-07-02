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
	"sync"
)

// DefaultBufSize is the portable fallback buffer used when no device-adaptive
// size has been chosen.
const DefaultBufSize = 1 << 20 // 1 MiB

// CopyOptions controls a single file copy.
type CopyOptions struct {
	Preserve bool // preserve mode/times/owner (and later xattr/ACL).
	Fsync    bool // fsync the temp file before the atomic rename.
	BufSize  int  // copy buffer size; <= 0 uses DefaultBufSize.

	// Progress, if non-nil, is called with the number of bytes newly written as
	// a copy proceeds — usually many times for a large file. It exists so callers
	// can observe live throughput and detect a genuine stall while a big file is
	// in flight (rather than only learning the file's size once it finishes). It
	// may be called from the copying goroutine many times per file, so a shared
	// implementation must be safe for concurrent use.
	Progress func(n int64)
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

	n, err := copyData(tmp, in, info, o.BufSize, o.Progress)
	if err != nil {
		return n, fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}

	if err := tmp.Chmod(perm); err != nil && !metaUnsupported(err) {
		return n, fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if o.Preserve && info != nil {
		// Owner/xattr applied to the temp path so the rename publishes a fully
		// formed file; times are applied after close (below) for the same reason.
		if err := preserveOwner(tmpName, info); err != nil {
			return n, err
		}
		if err := copyXattrs(src, tmpName, false); err != nil {
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

// bufPool recycles copy buffers across files so a run over many files does not
// allocate (and page-fault in) a fresh multi-megabyte buffer per file. Buffers of
// mixed capacity may coexist; a pooled buffer too small for a request is dropped
// and replaced, so the pool converges to the largest size in use.
var bufPool sync.Pool

func getBuf(size int) *[]byte {
	if v := bufPool.Get(); v != nil {
		b := v.(*[]byte)
		if cap(*b) >= size {
			*b = (*b)[:size]
			return b
		}
	}
	b := make([]byte, size)
	return &b
}

func putBuf(b *[]byte) { bufPool.Put(b) }

// plainCopy is the portable buffered fallback: a streamed copy from the current
// offsets of src to dst in bufSize chunks. It is shared by the platform copyData
// functions. It deliberately uses an explicit read/write loop rather than
// io.CopyBuffer: io.CopyBuffer would delegate to *os.File's ReaderFrom and copy
// through its own fixed 32 KiB buffer, ignoring bufSize entirely (32x the
// intended syscall count) — and the loop is also where progress is reported.
func plainCopy(dst, src *os.File, bufSize int, progress func(int64)) (int64, error) {
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	bp := getBuf(bufSize)
	defer putBuf(bp)
	buf := *bp

	var written int64
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
				if progress != nil {
					progress(int64(nw))
				}
			}
			if werr != nil {
				return written, werr
			}
			if nw < nr {
				return written, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
}
