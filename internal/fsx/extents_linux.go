//go:build linux

package fsx

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FIEMAP is in linux/fiemap.h. golang.org/x/sys has no binding for it.
const (
	fsIocFiemap    = 0xC020660B
	fiemapFlagSync = 0x1

	fiemapExtentLast   = 0x1
	fiemapExtentShared = 0x2000
	// UNKNOWN, DELALLOC, ENCODED, ENCRYPTED, NOT_ALIGNED, INLINE, TAIL, UNWRITTEN.
	fiemapExtentUnusable = 0x2 | 0x4 | 0x8 | 0x80 | 0x100 | 0x200 | 0x400 | 0x800
)

const fiemapBatch = 128

type fiemapExtent struct {
	Logical, Physical, Length uint64
	_                         [2]uint64
	Flags                     uint32
	_                         [3]uint32
}

type fiemapReq struct {
	Start, Length              uint64
	Flags, Mapped, Count, Resv uint32
	Ext                        [fiemapBatch]fiemapExtent
}

type extent struct {
	logical, physical, length int64
	flags                     uint32
}

// shareable reports whether e is shared and its physical range holds its bytes as they are.
func (e extent) shareable() bool {
	return e.flags&fiemapExtentShared != 0 && e.flags&fiemapExtentUnusable == 0
}

// fiemap returns the extents of f that overlap [start, start+length), in logical
// order. FIEMAP_FLAG_SYNC flushes dirty pages first, so they have real addresses.
func fiemap(f *os.File, start, length int64) ([]extent, error) {
	var (
		out []extent
		req fiemapReq
	)
	pos, end := uint64(start), uint64(start)+uint64(length)
	for pos < end {
		req = fiemapReq{Start: pos, Length: end - pos, Flags: fiemapFlagSync, Count: fiemapBatch}
		_, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), fsIocFiemap, uintptr(unsafe.Pointer(&req)))
		if e != 0 {
			return nil, e
		}
		if req.Mapped == 0 {
			break
		}
		for _, x := range req.Ext[:req.Mapped] {
			out = append(out, extent{int64(x.Logical), int64(x.Physical), int64(x.Length), x.Flags})
			if x.Flags&fiemapExtentLast != 0 {
				return out, nil
			}
		}
		last := req.Ext[req.Mapped-1]
		pos = last.Logical + last.Length
	}
	return out, nil
}

// segment is a part of the file: a piece of a shareable extent, or bytes to copy.
type segment struct {
	off, length int64
	phys        int64 // valid when shared
	shared      bool
}

// planSegments covers the file with the shareable extents of exts and copy
// ranges between them. A gap with no extent is still copied: a cloud
// placeholder reports no extents but reads real bytes.
func planSegments(exts []extent, size int64) []segment {
	var segs []segment
	pos := int64(0)
	for _, e := range exts {
		lo, hi := max(e.logical, pos), min(e.logical+e.length, size)
		if hi <= lo || !e.shareable() {
			continue
		}
		if lo > pos {
			segs = append(segs, segment{off: pos, length: lo - pos})
		}
		segs = append(segs, segment{off: lo, length: hi - lo, phys: e.physical + (lo - e.logical), shared: true})
		pos = hi
	}
	if pos < size {
		segs = append(segs, segment{off: pos, length: size - pos})
	}
	return segs
}

// errSourceChanged means the source moved during an extent-aware copy.
var errSourceChanged = errors.New("source changed during copy")

// copyDedup copies src to dst one segment at a time. It clones a shared extent
// whose bytes are already on the destination, and records each shared extent
// it writes. ok=false means dedup does not apply here, and dst is untouched.
func copyDedup(dst, src *os.File, size int64, o CopyOptions, fd *fileDedup) (int64, bool, error) {
	d := o.Dedup
	if d == nil || fd == nil {
		return 0, false, nil
	}
	var sst, dstSt unix.Stat_t
	if unix.Fstat(int(src.Fd()), &sst) != nil || unix.Fstat(int(dst.Fd()), &dstSt) != nil {
		return 0, false, nil
	}
	if !d.cloneProbe(dstSt.Dev, func() bool { return probeClone(dst) }) {
		return 0, false, nil
	}
	exts, err := fiemapFn(src, 0, size)
	if err != nil {
		return 0, false, nil
	}
	segs := planSegments(exts, size)
	if !anyShared(segs) {
		return 0, false, nil
	}
	dupFd, err := unix.Dup(int(dst.Fd()))
	if err != nil {
		return 0, false, nil
	}
	fd.srcDev = sst.Dev
	fd.ref = &destRef{f: os.NewFile(uintptr(dupFd), dst.Name()), dev: dstSt.Dev, ino: dstSt.Ino}

	var cloned []segment
	for _, s := range segs {
		if o.canceled() {
			return 0, true, ErrCanceled
		}
		if !s.shared {
			if err := copyRange(dst, src, s.off, s.length, o); err != nil {
				return 0, true, err
			}
			continue
		}
		ok, err := copyShared(d, dst, src, fd, s, o)
		if err != nil {
			return 0, true, err
		}
		if ok {
			cloned = append(cloned, s)
			if o.Progress != nil {
				o.Progress(s.length)
			}
		}
	}
	if err := dst.Truncate(size); err != nil {
		return 0, true, err
	}
	// A clone trusts that a physical address names the bytes that were there at
	// plan time.
	if !unchanged(src, size, &sst, cloned) {
		return 0, true, errSourceChanged
	}
	for _, s := range cloned {
		d.cloned.Add(s.length)
	}
	return size, true, nil
}

// copyShared puts a shared segment on dst. It clones the segment when another
// copy of its bytes is on the destination, else it copies and records them.
// It reports whether it cloned.
func copyShared(d *Dedup, dst, src *os.File, fd *fileDedup, s segment, o CopyOptions) (bool, error) {
	for {
		loc, delta, c, mine := d.acquire(fd.srcDev, s.phys, s.length)
		switch {
		case c == nil:
			if loc.ref.dev == fd.ref.dev && cloneFrom(dst, loc, delta, s) == nil {
				return true, nil
			}
			if err := copyRange(dst, src, s.off, s.length, o); err != nil {
				return false, err
			}
			if l, ok := record(src, fd, s); ok {
				d.insert(fd.srcDev, l)
			}
			return false, nil
		case !mine:
			// Another worker copies these bytes now. A clone after it is cheaper than a second copy.
			if !wait(c, o) {
				return false, ErrCanceled
			}
		default:
			if err := copyRange(dst, src, s.off, s.length, o); err != nil {
				d.release(c, nil)
				return false, err
			}
			if l, ok := record(src, fd, s); ok {
				d.release(c, &l)
			} else {
				d.release(c, nil)
			}
			return false, nil
		}
	}
}

// record returns the index entry for a segment that dst now holds. It checks
// again that the source still maps s to s.phys. If the extent moved during the
// read, the bytes on dst may not be the bytes at s.phys.
func record(src *os.File, fd *fileDedup, s segment) (extentLoc, bool) {
	exts, err := fiemapFn(src, s.off, s.length)
	if err != nil || !mapped(exts, s) {
		return extentLoc{}, false
	}
	l := extentLoc{phys: s.phys, length: s.length, off: s.off, ref: fd.ref}
	fd.recs = append(fd.recs, l)
	return l, true
}

func anyShared(segs []segment) bool {
	for _, s := range segs {
		if s.shared {
			return true
		}
	}
	return false
}

// unchanged reports whether src still has its plan-time size, mtime and the
// physical placement of every segment in segs.
func unchanged(src *os.File, size int64, before *unix.Stat_t, segs []segment) bool {
	var st unix.Stat_t
	if unix.Fstat(int(src.Fd()), &st) != nil || st.Size != size || st.Mtim != before.Mtim {
		return false
	}
	if len(segs) == 0 {
		return true
	}
	exts, err := fiemapFn(src, 0, size)
	if err != nil {
		return false
	}
	for _, s := range segs {
		if !mapped(exts, s) {
			return false
		}
	}
	return true
}

// mapped reports whether one extent of exts places all of s at s.phys.
func mapped(exts []extent, s segment) bool {
	for _, e := range exts {
		if e.logical <= s.off && s.off+s.length <= e.logical+e.length {
			return e.shareable() && e.physical+(s.off-e.logical) == s.phys
		}
	}
	return false
}

// Seams for both ioctls, so tests can run the dedup logic on any filesystem.
var (
	fiemapFn     = fiemap
	cloneRangeFn = cloneRange
)

// cloneRange makes [dstOff, dstOff+length) of dst share the extents of [fromOff, ...) of from.
func cloneRange(dst, from *os.File, fromOff, length, dstOff int64) error {
	return unix.IoctlFileCloneRange(int(dst.Fd()), &unix.FileCloneRange{
		Src_fd:      int64(from.Fd()),
		Src_offset:  uint64(fromOff),
		Src_length:  uint64(length),
		Dest_offset: uint64(dstOff),
	})
}

func cloneFrom(dst *os.File, loc extentLoc, delta int64, s segment) error {
	return loc.ref.with(func(from *os.File) error {
		return cloneRangeFn(dst, from, loc.off+delta, s.length, s.off)
	})
}

// probeClone reports whether the filesystem of f takes range clones. It
// clones an empty range of f onto itself, which changes nothing.
func probeClone(f *os.File) bool {
	return cloneRangeFn(f, f, 0, 0, 0) == nil
}

func sameInode(f *os.File, dev, ino uint64) bool {
	var st unix.Stat_t
	return unix.Fstat(int(f.Fd()), &st) == nil && st.Dev == dev && st.Ino == ino
}

// copyRange copies [off, off+length) from src to the same offset in dst and
// leaves all-zero blocks as holes. A short read means the source shrank.
func copyRange(dst, src *os.File, off, length int64, o CopyOptions) error {
	bufSize := o.BufSize
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	bp := getBuf(bufSize)
	defer putBuf(bp)
	buf := *bp

	for length > 0 {
		if o.canceled() {
			return ErrCanceled
		}
		p := buf[:min(int64(len(buf)), length)]
		nr, err := src.ReadAt(p, off)
		if nr > 0 {
			if werr := writeNonZero(dst, p[:nr], off); werr != nil {
				return werr
			}
			off += int64(nr)
			length -= int64(nr)
			if o.Progress != nil {
				o.Progress(int64(nr))
			}
		}
		if err == io.EOF && length > 0 {
			return errSourceChanged
		}
		if err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}
