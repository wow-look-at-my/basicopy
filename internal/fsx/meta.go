package fsx

import (
	"errors"
	"fmt"
	"os"
)

// metaUnsupported reports whether err means the destination filesystem simply
// does not support a metadata operation -- e.g. chmod/chown/utimes on a FAT,
// exFAT, or NTFS volume (a common external-backup target). Such a failure is not
// fatal: the file's contents were copied and committed correctly, only the
// cosmetic metadata could not be applied, so the copy must still count as a
// success rather than discard good data. The standard library maps ENOSYS,
// ENOTSUP, and EOPNOTSUPP to errors.ErrUnsupported, so the check is portable.
func metaUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

// ApplyMeta applies preserved metadata (mode, owner, xattrs, times) to an
// existing path. It is used for directories and recreated symlinks; regular
// files get their metadata applied to the temp file inside CopyFile so the rename
// publishes a fully formed file. Mode and times are skipped for symlinks (the
// link itself has no meaningful mode, and portable lutimes is added later).
// Operations the destination filesystem cannot support are skipped rather than
// treated as errors.
func ApplyMeta(src, dst string, info os.FileInfo, preserve bool) error {
	if !preserve || info == nil {
		return nil
	}
	isSymlink := info.Mode()&os.ModeSymlink != 0
	if !isSymlink {
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil && !metaUnsupported(err) {
			return fmt.Errorf("chmod %s: %w", dst, err)
		}
	}
	if err := preserveOwner(dst, info); err != nil {
		return err
	}
	if err := copyXattrs(src, dst, isSymlink); err != nil {
		return err
	}
	if !isSymlink {
		return preserveTimes(dst, info)
	}
	return nil
}

// SyncAttrs applies selected metadata attributes from info to dst: the
// permission bits, the ownership, and/or the modification time. It is used for
// attribute-only touch-ups on entries whose content is already up to date.
// Ownership is best-effort (EPERM is tolerated) and operations the destination
// filesystem cannot support are skipped, matching ApplyMeta's policy.
func SyncAttrs(dst string, info os.FileInfo, mode, owner, times bool) error {
	if mode {
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil && !metaUnsupported(err) {
			return fmt.Errorf("chmod %s: %w", dst, err)
		}
	}
	if owner {
		if err := preserveOwner(dst, info); err != nil {
			return err
		}
	}
	if times {
		return preserveTimes(dst, info)
	}
	return nil
}

// preserveTimes sets dst's modification time to match info. Sub-second precision
// and access-time preservation are refined in the platform-specific files; this
// portable version sets mtime (and atime = mtime as a safe default). A filesystem
// that cannot store timestamps is tolerated (the copy is not failed over it).
func preserveTimes(dst string, info os.FileInfo) error {
	mt := info.ModTime()
	if err := os.Chtimes(dst, mt, mt); err != nil && !metaUnsupported(err) {
		return fmt.Errorf("set times on %s: %w", dst, err)
	}
	return nil
}
