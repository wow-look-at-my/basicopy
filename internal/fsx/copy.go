// Package fsx holds the low-level filesystem copy primitives: content copying
// (with crash-safe temp-file + atomic rename), sparse handling, preallocation,
// and metadata preservation. Fast in-kernel paths (copy_file_range, clonefile,
// CopyFileEx) are layered on in build-tagged files and are used when both sides
// are local files; copy.go is the portable core, and the same core carries a
// copy to or from a remote host over SFTP.
package fsx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// DefaultBufSize is the fallback buffer size when no device-adaptive size is chosen.
const DefaultBufSize = 1 << 20 // 1 MiB

// CopyOptions controls a single file copy.
type CopyOptions struct {
	Preserve bool // preserve mode/times/owner (and later xattr/ACL).
	Fsync    bool // fsync the temp file before the atomic rename.
	BufSize  int  // copy buffer size; <= 0 uses DefaultBufSize.

	// Progress, if set, is called with bytes newly written; may fire often and concurrently.
	Progress func(n int64)

	// Cancel, if set, is polled between chunks; true stops the copy early with ErrCanceled.
	Cancel func() bool
}

// ErrCanceled means CopyOptions.Cancel aborted the copy; the temp file is already removed.
var ErrCanceled = errors.New("copy canceled")

// canceled reports whether the caller asked in-flight copies to stop.
func (o *CopyOptions) canceled() bool { return o.Cancel != nil && o.Cancel() }

// CopyFile copies the regular file at src on srcFS to dst on dstFS, crash-safely:
// the bytes are written to a temporary file in dst's directory, metadata is
// applied, and the temp file is atomically renamed into place. An interrupted
// copy therefore never leaves a partial file at the final path. info is src's
// Lstat result and is used for metadata preservation. It returns the number of
// bytes copied.
func CopyFile(srcFS vfs.FS, src string, dstFS vfs.FS, dst string, info os.FileInfo, o CopyOptions) (int64, error) {
	if o.canceled() {
		return 0, ErrCanceled
	}
	in, err := srcFS.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	perm := os.FileMode(0o644)
	if info != nil {
		perm = info.Mode().Perm()
	}

	tmp, err := dstFS.CreateTemp(dstFS.Dir(dst), ".basicopy-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temp for %s: %w", dstFS.Display(dst), err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			dstFS.Remove(tmpName)
		}
	}()

	n, err := copyContents(tmp, in, info, o)
	if err != nil {
		return n, fmt.Errorf("copy %s -> %s: %w", srcFS.Display(src), dstFS.Display(dst), err)
	}

	if err := tmp.Chmod(perm); err != nil && !metaUnsupported(err) {
		return n, fmt.Errorf("chmod %s: %w", dstFS.Display(tmpName), err)
	}
	if o.Preserve && info != nil {
		// Owner/xattr apply to the temp path; rename then publishes a complete file.
		if err := preserveOwner(srcFS, info, dstFS, tmpName); err != nil {
			return n, err
		}
		if err := copyXattrsFS(srcFS, src, dstFS, tmpName, false); err != nil {
			return n, err
		}
	}
	if o.Fsync {
		if err := tmp.Sync(); err != nil && !metaUnsupported(err) {
			return n, fmt.Errorf("fsync %s: %w", dstFS.Display(tmpName), err)
		}
	}
	if err := tmp.Close(); err != nil {
		return n, fmt.Errorf("close %s: %w", dstFS.Display(tmpName), err)
	}

	if o.Preserve && info != nil {
		if err := preserveTimes(dstFS, tmpName, info); err != nil {
			return n, err
		}
	}

	if err := dstFS.Rename(tmpName, dst); err != nil {
		return n, fmt.Errorf("rename %s -> %s: %w", dstFS.Display(tmpName), dstFS.Display(dst), err)
	}
	committed = true
	return n, nil
}

// copyContents moves the bytes, taking the platform fast paths (reflink, sparse
// hole-skipping, copy_file_range) only when both ends are real local files. A
// remote end is an SFTP handle, so it takes the portable streaming path.
func copyContents(dst, src vfs.File, info os.FileInfo, o CopyOptions) (int64, error) {
	dstFile, dstLocal := dst.(*os.File)
	srcFile, srcLocal := src.(*os.File)
	if dstLocal && srcLocal {
		return copyData(dstFile, srcFile, info, o)
	}
	return plainCopy(dst, src, o)
}

// bufPool recycles copy buffers; a too-small pooled buffer is dropped and replaced.
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

// plainCopy streams src to dst in o.BufSize chunks; used for remote ends and as
// the portable fallback. An explicit loop, not io.CopyBuffer: io.CopyBuffer
// would delegate to *os.File's ReaderFrom and ignore BufSize (via its own fixed
// 32 KiB buffer), and it has no hook for progress or cancellation.
func plainCopy(dst io.Writer, src io.Reader, o CopyOptions) (int64, error) {
	bufSize := o.BufSize
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	bp := getBuf(bufSize)
	defer putBuf(bp)
	buf := *bp

	var written int64
	for {
		if o.canceled() {
			return written, ErrCanceled
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
				if o.Progress != nil {
					o.Progress(int64(nw))
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
