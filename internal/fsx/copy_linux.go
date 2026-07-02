//go:build linux

package fsx

import (
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// copyData copies info.Size() bytes from src to dst using the fastest correct
// method available, in order of preference:
//
//  1. FICLONE whole-file reflink — instant, shares extents (same-fs CoW: btrfs,
//     XFS-reflink, bcachefs). Independent inode/metadata; just storage-shared.
//  2. Sparse-aware copy (SEEK_DATA/SEEK_HOLE) — preserves holes for sparse files.
//  3. copy_file_range — in-kernel copy (and NFS server-side copy), no userspace
//     bounce buffer.
//  4. Buffered read/write — the universal fallback.
func copyData(dst, src *os.File, info os.FileInfo, bufSize int, progress func(int64)) (int64, error) {
	if info == nil {
		return plainCopy(dst, src, bufSize, progress)
	}
	size := info.Size()

	if size > 0 {
		if err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err == nil {
			// A reflink shares extents instantly; credit the whole logical size so
			// live progress reflects that the file has been copied.
			if progress != nil {
				progress(size)
			}
			return size, nil
		}
	}

	if isSparse(info) {
		if n, ok, err := copySparse(dst, src, size, bufSize, progress); ok {
			return n, err
		}
	}

	if n, ok, err := copyFileRange(dst, src, size, progress); ok {
		return n, err
	}

	return plainCopy(dst, src, bufSize, progress)
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

// copyFileRange copies up to size bytes via copy_file_range. ok=false means the
// call is unsupported for this pair (cross-fs, old kernel, special file) and the
// caller should fall back; it only reports unsupported before any bytes move.
func copyFileRange(dst, src *os.File, size int64, progress func(int64)) (copied int64, ok bool, err error) {
	// Cap each call so the int64->int conversion can't overflow on 32-bit
	// platforms (or for multi-GB files); the loop handles the remainder. The cap
	// also bounds how long a single syscall runs before progress is reported, so
	// a large file in flight never looks like a stall to the watchdog — even on
	// slow media (128 MiB is ~4 s at a glacial 30 MB/s, well under the timeout).
	const maxChunk = 128 << 20 // 128 MiB
	remaining := size
	for remaining > 0 {
		chunk := remaining
		if chunk > maxChunk {
			chunk = maxChunk
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
		if progress != nil {
			progress(int64(n))
		}
	}
	return copied, true, nil
}

// copySparse copies a file while preserving its holes, using SEEK_DATA/SEEK_HOLE
// to find data regions and skipping the gaps. ok=false (returned before any
// write) means the filesystem doesn't support hole-seeking and the caller should
// fall back. On success it returns the logical size.
func copySparse(dst, src *os.File, size int64, bufSize int, progress func(int64)) (copied int64, ok bool, err error) {
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	bp := getBuf(bufSize)
	defer putBuf(bp)
	buf := *bp
	srcFd := int(src.Fd())

	var offset int64
	for offset < size {
		dataStart, e := unix.Seek(srcFd, offset, unix.SEEK_DATA)
		if e != nil {
			if e == unix.ENXIO {
				break // no more data; the remainder is a hole
			}
			return 0, false, nil // SEEK_DATA unsupported -> fall back
		}
		holeStart, e := unix.Seek(srcFd, dataStart, unix.SEEK_HOLE)
		if e != nil || holeStart > size {
			holeStart = size
		}
		if _, e := unix.Seek(srcFd, dataStart, unix.SEEK_SET); e != nil {
			return copied, true, e
		}
		if _, e := dst.Seek(dataStart, io.SeekStart); e != nil {
			return copied, true, e
		}
		remaining := holeStart - dataStart
		for remaining > 0 {
			r := int64(bufSize)
			if r > remaining {
				r = remaining
			}
			nr, rerr := src.Read(buf[:r])
			if nr > 0 {
				if _, werr := dst.Write(buf[:nr]); werr != nil {
					return copied, true, werr
				}
				copied += int64(nr)
				remaining -= int64(nr)
				if progress != nil {
					progress(int64(nr))
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return copied, true, rerr
			}
		}
		offset = holeStart
	}

	// Set the exact size so a trailing hole is represented and length is correct.
	if e := dst.Truncate(size); e != nil {
		return copied, true, e
	}
	return size, true, nil
}
