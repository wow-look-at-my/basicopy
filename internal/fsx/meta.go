package fsx

import (
	"errors"
	"fmt"
	"os"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// metaUnsupported reports whether err means the destination filesystem simply
// does not support a metadata operation -- e.g. chmod/chown/utimes on a FAT,
// exFAT, or NTFS volume (a common external-backup target), or an SFTP server
// that rejects an optional extension. Such a failure is not fatal: the file's
// contents were copied and committed correctly, only the cosmetic metadata could
// not be applied, so the copy must still count as a success rather than discard
// good data. The standard library maps ENOSYS, ENOTSUP, and EOPNOTSUPP to
// errors.ErrUnsupported, so the check is portable.
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
func ApplyMeta(srcFS vfs.FS, src string, dstFS vfs.FS, dst string, info os.FileInfo, preserve bool) error {
	if !preserve || info == nil {
		return nil
	}
	isSymlink := info.Mode()&os.ModeSymlink != 0
	if !isSymlink {
		if err := dstFS.Chmod(dst, info.Mode().Perm()); err != nil && !metaUnsupported(err) {
			return fmt.Errorf("chmod %s: %w", dstFS.Display(dst), err)
		}
	}
	if err := preserveOwner(srcFS, info, dstFS, dst); err != nil {
		return err
	}
	if err := copyXattrsFS(srcFS, src, dstFS, dst, isSymlink); err != nil {
		return err
	}
	if !isSymlink {
		return preserveTimes(dstFS, dst, info)
	}
	return nil
}

// SyncAttrs applies selected metadata attributes from info to dst: the
// permission bits, the ownership, and/or the modification time. It is used for
// attribute-only touch-ups on entries whose content is already up to date.
// Ownership is best-effort (EPERM is tolerated) and operations the destination
// filesystem cannot support are skipped, matching ApplyMeta's policy.
func SyncAttrs(srcFS vfs.FS, info os.FileInfo, dstFS vfs.FS, dst string, mode, owner, times bool) error {
	if mode {
		if err := dstFS.Chmod(dst, info.Mode().Perm()); err != nil && !metaUnsupported(err) {
			return fmt.Errorf("chmod %s: %w", dstFS.Display(dst), err)
		}
	}
	if owner {
		if err := preserveOwner(srcFS, info, dstFS, dst); err != nil {
			return err
		}
	}
	if times {
		return preserveTimes(dstFS, dst, info)
	}
	return nil
}

// preserveOwner sets dst's uid/gid to match the source entry. It is best-effort:
// lacking privilege (EPERM) is the common case for unprivileged copies, a
// destination filesystem without ownership (FAT/exFAT/NTFS) reports
// "unsupported", and a source that carries no ownership at all (Windows) leaves
// nothing to apply; none of those are treated as fatal.
func preserveOwner(srcFS vfs.FS, info os.FileInfo, dstFS vfs.FS, dst string) error {
	uid, gid, ok := srcFS.Owner(info)
	if !ok {
		return nil
	}
	if err := dstFS.Chown(dst, uid, gid); err != nil {
		if os.IsPermission(err) || metaUnsupported(err) {
			return nil
		}
		return err
	}
	return nil
}

// preserveTimes sets dst's modification time to match info. Sub-second precision
// is limited by what the destination can store (SFTP version 3 carries
// whole-second times, which is why the unchanged-file comparison tolerates a
// one-second difference). A filesystem that cannot store timestamps is tolerated
// (the copy is not failed over it).
func preserveTimes(dstFS vfs.FS, dst string, info os.FileInfo) error {
	mt := info.ModTime()
	if err := dstFS.Chtimes(dst, mt, mt); err != nil && !metaUnsupported(err) {
		return fmt.Errorf("set times on %s: %w", dstFS.Display(dst), err)
	}
	return nil
}

// copyXattrsFS copies extended attributes, but only for a local-to-local copy:
// SFTP has no portable xattr operations, so a copy with a remote end preserves
// the ordinary metadata and leaves xattrs (and Linux ACLs, which ride on them)
// behind.
func copyXattrsFS(srcFS vfs.FS, src string, dstFS vfs.FS, dst string, nofollow bool) error {
	if !srcFS.Local() || !dstFS.Local() {
		return nil
	}
	return copyXattrs(src, dst, nofollow)
}
