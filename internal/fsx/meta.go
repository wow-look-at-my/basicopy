package fsx

import (
	"fmt"
	"os"
)

// ApplyMeta applies preserved metadata (mode, owner, times) to an existing path.
// It is used for directories and recreated symlinks; regular files get their
// metadata applied to the temp file inside CopyFile so the rename publishes a
// fully formed file. Mode and times are skipped for symlinks (the link itself
// has no meaningful mode, and portable lutimes is added later).
func ApplyMeta(path string, info os.FileInfo, preserve bool) error {
	if !preserve || info == nil {
		return nil
	}
	isSymlink := info.Mode()&os.ModeSymlink != 0
	if !isSymlink {
		if err := os.Chmod(path, info.Mode().Perm()); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	if err := preserveOwner(path, info); err != nil {
		return err
	}
	if !isSymlink {
		return preserveTimes(path, info)
	}
	return nil
}

// preserveTimes sets dst's modification time to match info. Sub-second precision
// and access-time preservation are refined in the platform-specific files; this
// portable version sets mtime (and atime = mtime as a safe default).
func preserveTimes(dst string, info os.FileInfo) error {
	mt := info.ModTime()
	if err := os.Chtimes(dst, mt, mt); err != nil {
		return fmt.Errorf("set times on %s: %w", dst, err)
	}
	return nil
}
