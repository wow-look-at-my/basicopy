// Package scan decides whether a destination file already matches its source and
// can be skipped. The default quick check compares size and mtime (cheap, and
// reliable because basicopy preserves mtime on copy); --checksum compares BLAKE3
// content hashes instead.
package scan

import (
	"io"
	"io/fs"
	"os"
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

func hashFile(path string) ([32]byte, error) {
	var sum [32]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := blake3.New(32, nil)
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
