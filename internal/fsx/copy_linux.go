//go:build linux

package fsx

import (
	"bytes"
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// copyData copies info.Size() bytes from src to dst using the fastest correct
// method available, in order of preference:
//
//	FICLONE whole-file reflink — instant, shares extents (same-fs CoW: btrfs,
//	   XFS-reflink, bcachefs). Independent inode/metadata; just storage-shared.
//	Extent-aware copy -- a shared source extent that is already on the
//	   destination is cloned there with FICLONERANGE (copyDedup).
//	   Sparse-aware copy (all-zero blocks skipped) — preserves holes for sparse files.
func copyData(dst, src *os.File, info os.FileInfo, o CopyOptions, fd *fileDedup) (int64, error) {
	if info == nil {
		return plainCopy(dst, src, o)
	}
	size := info.Size()

	if size > 0 {
		if err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err == nil {
			// A reflink shares extents instantly; credit the whole logical size so
			// live progress reflects that the file has been copied.
			if o.Progress != nil {
				o.Progress(size)
			}
			return size, nil
		}
		if n, ok, err := copyDedup(dst, src, size, o, fd); ok {
			if !errors.Is(err, errSourceChanged) {
				return n, err
			}
			// Nothing from the extent pass is trusted; copy the whole file again.
			fd.kill()
			if err := dst.Truncate(0); err != nil {
				return 0, err
			}
		}
	}

	if isSparse(info) {
		return copySparse(dst, src, o)
	}

	if n, ok, err := copyFileRange(dst, src, size, o); ok {
		return n, err
	}

	return plainCopy(dst, src, o)
}

// isSparse reports whether info's allocated blocks are fewer than its logical
// size implies, i.e. the file has holes worth preserving.
func isSparse(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int64(st.Blocks)*512 < info.Size()
}

// copyChunk bounds each copy_file_range call, against overflow and slow progress/cancel checks.
var copyChunk = int64(128 << 20)

// copyFileRange copies up to size bytes via copy_file_range. ok=false means the
// call is unsupported for this pair (cross-fs, old kernel, special file) and the
// caller should fall back; it only reports unsupported before any bytes move.
func copyFileRange(dst, src *os.File, size int64, o CopyOptions) (copied int64, ok bool, err error) {
	remaining := size
	for remaining > 0 {
		if o.canceled() {
			return copied, true, ErrCanceled
		}
		chunk := remaining
		if chunk > copyChunk {
			chunk = copyChunk
		}
		n, e := unix.CopyFileRange(int(src.Fd()), nil, int(dst.Fd()), nil, int(chunk), 0)
		if e != nil {
			if copied == 0 {
				switch e {
				case unix.ENOSYS, unix.EXDEV, unix.EINVAL, unix.EOPNOTSUPP, unix.EBADF, unix.EPERM:
					return 0, false, nil
				}
			}
			return copied, true, e
		}
		if n == 0 {
			break
		}
		copied += int64(n)
		remaining -= int64(n)
		if o.Progress != nil {
			o.Progress(int64(n))
		}
	}
	return copied, true, nil
}

// sparseBlock is the unit of hole detection: an all-zero block is not written, so it stays a hole in the destination.
const sparseBlock = 4096

var zeroBlock [sparseBlock]byte

// copySparse reads every byte of src and writes only the blocks that are not
// all zeros, so holes survive. It never trusts SEEK_DATA or the walk-time size.
// A cloud placeholder over SMB or FUSE reports no data extents but reads real
// bytes, and a source can shrink after the walk.
func copySparse(dst, src *os.File, o CopyOptions) (int64, error) {
	bufSize := o.BufSize
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	bp := getBuf(bufSize)
	defer putBuf(bp)
	buf := *bp

	var offset int64
	for {
		if o.canceled() {
			return offset, ErrCanceled
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			if err := writeNonZero(dst, buf[:nr], offset); err != nil {
				return offset, err
			}
			offset += int64(nr)
			if o.Progress != nil {
				o.Progress(int64(nr))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return offset, rerr
		}
	}

	// The length is what read() returned, so a trailing hole gets its true size.
	if err := dst.Truncate(offset); err != nil {
		return offset, err
	}
	return offset, nil
}

// writeNonZero writes p at off, one run of non-zero blocks at a time.
func writeNonZero(dst *os.File, p []byte, off int64) error {
	runStart := -1
	for i := 0; i < len(p); i += sparseBlock {
		end := min(i+sparseBlock, len(p))
		zero := bytes.Equal(p[i:end], zeroBlock[:end-i])
		if !zero && runStart < 0 {
			runStart = i
		}
		if zero && runStart >= 0 {
			if _, err := dst.WriteAt(p[runStart:i], off+int64(runStart)); err != nil {
				return err
			}
			runStart = -1
		}
	}
	if runStart >= 0 {
		if _, err := dst.WriteAt(p[runStart:], off+int64(runStart)); err != nil {
			return err
		}
	}
	return nil
}
