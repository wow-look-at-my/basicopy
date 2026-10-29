// metaUnsupported reports whether err is a benign missing-metadata-support error.

package fsx

import (
	"fmt"
	"os"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// metaUnsupported reports whether err is a benign missing-metadata-support error.
func metaUnsupported(err error) bool {
	return vfs.Unsupported(err)
}

// ApplyMeta applies preserved metadata (mode, owner, xattrs, times) to an
// existing path. It is used for directories and recreated symlinks. Regular files
// get their metadata applied to the temp file inside CopyFile so the rename
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

// preserveOwner sets dst's uid/gid to match src. EPERM (unprivileged copy),
// unsupported (FAT/exFAT/NTFS), and no source ownership (Windows) are not fatal.
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

// preserveTimes sets dst's mtime to match info.
func preserveTimes(dstFS vfs.FS, dst string, info os.FileInfo) error {
	mt := info.ModTime()
	if err := dstFS.Chtimes(dst, mt, mt); err != nil && !metaUnsupported(err) {
		return fmt.Errorf("set times on %s: %w", dstFS.Display(dst), err)
	}
	return nil
}

// copyXattrsFS copies xattrs only for local-to-local copies; SFTP has no
// portable xattr operations, so a remote-end copy leaves xattrs (and ACLs) behind.
func copyXattrsFS(srcFS vfs.FS, src string, dstFS vfs.FS, dst string, nofollow bool) error {
	if !srcFS.Local() || !dstFS.Local() {
		return nil
	}
	return copyXattrs(src, dst, nofollow)
}
