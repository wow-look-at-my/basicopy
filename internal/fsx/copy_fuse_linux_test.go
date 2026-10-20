//go:build linux

package fsx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// placeholder acts like a dehydrated cloud file: no allocated blocks and no
// data extents, but a read returns the real bytes.
type placeholder struct {
	fs.Inode
	data []byte
}

var (
	_ fs.NodeGetattrer = (*placeholder)(nil)
	_ fs.NodeOpener    = (*placeholder)(nil)
	_ fs.NodeReader    = (*placeholder)(nil)
	_ fs.NodeLseeker   = (*placeholder)(nil)
)

func (p *placeholder) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFREG | 0o644
	out.Size = uint64(len(p.data))
	out.Blksize = 4096
	out.Blocks = 0
	return 0
}

func (p *placeholder) Open(context.Context, uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (p *placeholder) Read(_ context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(p.data)) {
		return fuse.ReadResultData(nil), 0
	}
	return fuse.ReadResultData(p.data[off:min(off+int64(len(dest)), int64(len(p.data)))]), 0
}

func (p *placeholder) Lseek(_ context.Context, _ fs.FileHandle, off uint64, whence uint32) (uint64, syscall.Errno) {
	if whence == 3 { // SEEK_DATA
		return 0, syscall.ENXIO
	}
	return off, 0
}

type placeholderRoot struct {
	fs.Inode
	file *placeholder
}

func (r *placeholderRoot) OnAdd(ctx context.Context) {
	r.AddChild("cloud.bin", r.NewPersistentInode(ctx, r.file, fs.StableAttr{Mode: syscall.S_IFREG}), false)
}

// A cloud placeholder looks sparse and reports no data extents. The copy must
// still hold the bytes that read() returns, not a file of zeros.
func TestCopyFileCloudPlaceholder(t *testing.T) {
	want := bytes.Repeat([]byte("cloud-bytes!"), 100_000)
	mnt := t.TempDir()
	server, err := fs.Mount(mnt, &placeholderRoot{file: &placeholder{data: want}}, &fs.Options{
		MountOptions: fuse.MountOptions{DirectMount: true, DirectMountStrict: true},
	})
	if err != nil {
		t.Skipf("cannot mount FUSE here (needs /dev/fuse and root): %v", err)
	}
	t.Cleanup(func() { server.Unmount() })

	src := filepath.Join(mnt, "cloud.bin")
	info, err := os.Lstat(src)
	require.NoError(t, err)
	require.True(t, isSparse(info), "the placeholder must take the sparse path")

	dst := filepath.Join(t.TempDir(), "cloud.out")
	n, err := CopyFile(vfs.Local, src, vfs.Local, dst, info, CopyOptions{})
	require.NoError(t, err)
	assert.EqualValues(t, len(want), n)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(want, got), "the copy must hold the placeholder's bytes, not zeros")
}
