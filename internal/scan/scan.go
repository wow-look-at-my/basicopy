// Package scan decides whether a destination file already matches its source and
// can be skipped. The default quick check compares size and mtime (cheap, and
// reliable because basicopy preserves mtime on copy); --checksum compares BLAKE3
// content hashes instead. Compare returns a reason-coded verdict so callers can
// itemize why a file will be copied -- and, for a file whose content is already
// up to date, which attributes have drifted and still need a touch-up.
//
// Either side may live on a remote host: the comparison goes through the vfs
// seam, so --checksum over SSH reads the remote file's bytes to hash them (there
// is no remote hashing agent), which is why size+mtime remains the default.
package scan

import (
	"fmt"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
	"lukechampine.com/blake3"
)

// mtimeTolerance absorbs sub-second/granularity differences between filesystems
// -- including SFTP, whose version-3 attributes carry whole seconds only.
const mtimeTolerance = time.Second

// Verdict is the reason-coded result of comparing a source file with its
// destination path.
type Verdict struct {
	// NeedCopy reports that the destination must be (re)written; Reason then
	// holds the printable cause: "new" (destination missing), "type change"
	// (destination exists but is not a regular file), "size OLD -> NEW",
	// "mtime differs" (default mode), or "content differs" (checksum mode).
	NeedCopy bool
	Reason   string

	// DstInfo is the destination's Lstat result, returned so callers don't
	// re-stat. It is nil when the destination is missing.
	DstInfo fs.FileInfo

	// Attribute drift on an up-to-date destination (only meaningful when
	// !NeedCopy): permission bits, Unix owner uid:gid (false when either side
	// cannot report ownership), and mtime. TimeDiff is reported only in
	// checksum mode, where content equality is proven and the mtimes still
	// disagree beyond the tolerance; in default mode "unchanged" already
	// implies the mtimes matched.
	ModeDiff  bool
	OwnerDiff bool
	TimeDiff  bool

	// Owner values for rendering an owner diff (valid when OwnerDiff).
	SrcUID, SrcGID int
	DstUID, DstGID int
}

// Compare decides whether dstPath is an up-to-date copy of the source described
// by srcPath/srcInfo, and why not when it isn't. Content that cannot be read
// hashes as different, so an error still results in a copy attempt (which then
// surfaces the real error).
func Compare(srcFS vfs.FS, srcPath string, srcInfo fs.FileInfo, dstFS vfs.FS, dstPath string, checksum bool) Verdict {
	dstInfo, err := dstFS.Lstat(dstPath)
	if err != nil {
		return Verdict{NeedCopy: true, Reason: "new"}
	}
	if !dstInfo.Mode().IsRegular() {
		return Verdict{NeedCopy: true, Reason: "type change", DstInfo: dstInfo}
	}
	if srcInfo.Size() != dstInfo.Size() {
		return Verdict{
			NeedCopy: true,
			Reason:   fmt.Sprintf("size %d -> %d", dstInfo.Size(), srcInfo.Size()),
			DstInfo:  dstInfo,
		}
	}
	mtimeMatch := absDur(srcInfo.ModTime().Sub(dstInfo.ModTime())) <= mtimeTolerance
	if !checksum {
		if !mtimeMatch {
			return Verdict{NeedCopy: true, Reason: "mtime differs", DstInfo: dstInfo}
		}
	} else if !sameContent(srcFS, srcPath, dstFS, dstPath) {
		return Verdict{NeedCopy: true, Reason: "content differs", DstInfo: dstInfo}
	}

	v := CompareAttrs(srcFS, srcInfo, dstFS, dstInfo)
	v.TimeDiff = checksum && !mtimeMatch
	return v
}

// CompareAttrs reports attribute-only drift (permission bits and, where both
// sides report it, the owner uid:gid) between a source entry and an existing
// destination entry. It is the "unchanged, but..." half of Compare, exposed
// separately so directory walks can itemize the same drift for existing
// directories.
func CompareAttrs(srcFS vfs.FS, srcInfo fs.FileInfo, dstFS vfs.FS, dstInfo fs.FileInfo) Verdict {
	v := Verdict{DstInfo: dstInfo}
	v.ModeDiff = srcInfo.Mode().Perm() != dstInfo.Mode().Perm()
	if su, sg, ok := srcFS.Owner(srcInfo); ok {
		if du, dg, ok := dstFS.Owner(dstInfo); ok && (su != du || sg != dg) {
			v.OwnerDiff = true
			v.SrcUID, v.SrcGID = su, sg
			v.DstUID, v.DstGID = du, dg
		}
	}
	return v
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// sameContent reports whether both files hash to the same BLAKE3 digest. An
// unreadable file reports "different" so the copy path surfaces the real error.
func sameContent(srcFS vfs.FS, srcPath string, dstFS vfs.FS, dstPath string) bool {
	hs, err := hashFile(srcFS, srcPath)
	if err != nil {
		return false
	}
	hd, err := hashFile(dstFS, dstPath)
	if err != nil {
		return false
	}
	return hs == hd
}

// hashBufSize is the read chunk for content hashing. Reading in large chunks
// matters twice over: it divides the read() syscall count (and, on a remote
// file, keeps many SFTP requests in flight per read), and BLAKE3 hashes large
// writes in-place where io.Copy's small internal buffer made the hasher allocate
// per chunk (more garbage than the file itself).
const hashBufSize = 1 << 20

var bufPool = sync.Pool{New: func() any { b := make([]byte, hashBufSize); return &b }}

func hashFile(vfsys vfs.FS, path string) ([32]byte, error) {
	var sum [32]byte
	f, err := vfsys.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := blake3.New(32, nil)
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	for {
		n, rerr := f.Read(*bp)
		if n > 0 {
			h.Write((*bp)[:n]) // a hash.Hash never returns a write error
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return sum, rerr
		}
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
