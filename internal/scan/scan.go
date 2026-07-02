// Package scan decides whether a destination file already matches its source and
// can be skipped. The default quick check compares size and mtime (cheap, and
// reliable because basicopy preserves mtime on copy); --checksum compares BLAKE3
// content hashes instead.
package scan

import (
	"io"
	"io/fs"
	"os"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

// mtimeTolerance absorbs sub-second/granularity differences between filesystems.
const mtimeTolerance = time.Second

// Unchanged reports whether dstPath already matches the source described by
// srcPath/srcInfo and can be skipped. Any error (e.g. dst missing) means "not
// unchanged" — copy it.
func Unchanged(srcPath string, srcInfo fs.FileInfo, dstPath string, checksum bool) bool {
	dstInfo, err := os.Lstat(dstPath)
	if err != nil || !dstInfo.Mode().IsRegular() {
		return false
	}
	if srcInfo.Size() != dstInfo.Size() {
		return false
	}
	if !checksum {
		diff := srcInfo.ModTime().Sub(dstInfo.ModTime())
		if diff < 0 {
			diff = -diff
		}
		return diff <= mtimeTolerance
	}
	hs, err := hashFile(srcPath)
	if err != nil {
		return false
	}
	hd, err := hashFile(dstPath)
	if err != nil {
		return false
	}
	return hs == hd
}

// hashBufSize is the read chunk for content hashing. Reading in large chunks
// matters twice over: it divides the read() syscall count, and BLAKE3 hashes
// large writes in-place where io.Copy's small internal buffer made the hasher
// allocate per chunk (more garbage than the file itself).
const hashBufSize = 1 << 20

var bufPool = sync.Pool{New: func() any { b := make([]byte, hashBufSize); return &b }}

func hashFile(path string) ([32]byte, error) {
	var sum [32]byte
	f, err := os.Open(path)
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
