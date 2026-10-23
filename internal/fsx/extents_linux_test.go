//go:build linux

package fsx

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// fakeExtents stands in for FIEMAP and FICLONERANGE, so the dedup logic runs
// on any filesystem. A source file maps to the extents a test gives it.
type fakeExtents struct {
	mu       sync.Mutex
	byIno    map[uint64][]extent
	clones   atomic.Int64
	cloneErr error
	// onMap, if set, runs before each map of a source file and can change it.
	onMap func(ino uint64, calls int)
	calls int
}

func installFake(t *testing.T) *fakeExtents {
	fx := &fakeExtents{byIno: map[uint64][]extent{}}
	oldMap, oldClone := fiemapFn, cloneRangeFn
	fiemapFn, cloneRangeFn = fx.fiemap, fx.clone
	t.Cleanup(func() { fiemapFn, cloneRangeFn = oldMap, oldClone })
	return fx
}

func ino(t *testing.T, path string) uint64 {
	var st unix.Stat_t
	require.NoError(t, unix.Stat(path, &st))
	return st.Ino
}

func (fx *fakeExtents) set(t *testing.T, path string, exts ...extent) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.byIno[ino(t, path)] = exts
}

func (fx *fakeExtents) fiemap(f *os.File, start, length int64) ([]extent, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, err
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.onMap != nil {
		fx.calls++
		fx.onMap(st.Ino, fx.calls)
	}
	var out []extent
	for _, e := range fx.byIno[st.Ino] {
		if e.logical < start+length && start < e.logical+e.length {
			out = append(out, e)
		}
	}
	return out, nil
}

func (fx *fakeExtents) clone(dst, from *os.File, fromOff, length, dstOff int64) error {
	if fx.cloneErr != nil {
		return fx.cloneErr
	}
	if length == 0 {
		return nil
	}
	buf := make([]byte, length)
	if _, err := from.ReadAt(buf, fromOff); err != nil {
		return err
	}
	if _, err := dst.WriteAt(buf, dstOff); err != nil {
		return err
	}
	fx.clones.Add(1)
	return nil
}

func randBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func writeSrc(t *testing.T, path string, data []byte) {
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func copyOne(t *testing.T, src, dst string, o CopyOptions) {
	info, err := os.Lstat(src)
	require.NoError(t, err)
	_, err = CopyFile(vfs.Local, src, vfs.Local, dst, info, o)
	require.NoError(t, err)
	want, err := os.ReadFile(src)
	require.NoError(t, err)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, want, got, "content of %s must match its source", dst)
}

const shared = fiemapExtentShared | fiemapExtentLast

func TestDedupClonesSharedExtentsAcrossAndWithinFiles(t *testing.T) {
	fx := installFake(t)
	dir := t.TempDir()
	const size = 64 << 10
	const phys = 1 << 30
	data := randBytes(t, size)

	// a and b share one extent. c holds the first half of it, a couple of times.
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	writeSrc(t, a, data)
	writeSrc(t, b, data)
	writeSrc(t, c, append(append([]byte{}, data[:size/2]...), data[:size/2]...))
	fx.set(t, a, extent{0, phys, size, shared})
	fx.set(t, b, extent{0, phys, size, shared})
	fx.set(t, c, extent{0, phys, size / 2, fiemapExtentShared}, extent{size / 2, phys, size / 2, shared})

	o := CopyOptions{Dedup: NewDedup()}
	out := t.TempDir()
	for _, p := range []string{a, b, c} {
		copyOne(t, p, filepath.Join(out, filepath.Base(p)), o)
	}
	assert.EqualValues(t, 2*size, o.Dedup.Cloned(), "b and both halves of c must be clones of a")
	assert.EqualValues(t, 3, fx.clones.Load())
}

func TestDedupIntraFileDuplicate(t *testing.T) {
	fx := installFake(t)
	dir := t.TempDir()
	const half = 32 << 10
	part := randBytes(t, half)
	src := filepath.Join(dir, "twice")
	writeSrc(t, src, append(append([]byte{}, part...), part...))
	fx.set(t, src, extent{0, 4096, half, fiemapExtentShared}, extent{half, 4096, half, shared})

	o := CopyOptions{Dedup: NewDedup()}
	copyOne(t, src, filepath.Join(t.TempDir(), "twice"), o)
	assert.EqualValues(t, half, o.Dedup.Cloned(), "the second half must clone the first")
}

func TestDedupConcurrentCopiesShareOneTransfer(t *testing.T) {
	fx := installFake(t)
	dir, out := t.TempDir(), t.TempDir()
	const size = 1 << 20
	const n = 8
	data := randBytes(t, size)
	var paths []string
	for i := range n {
		p := filepath.Join(dir, string(rune('a'+i)))
		writeSrc(t, p, data)
		fx.set(t, p, extent{0, 1 << 30, size, shared})
		paths = append(paths, p)
	}

	o := CopyOptions{Dedup: NewDedup()}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Go(func() { copyOne(t, p, filepath.Join(out, filepath.Base(p)), o) })
	}
	wg.Wait()
	assert.EqualValues(t, (n-1)*size, o.Dedup.Cloned(), "one worker copies the extent; every other worker clones it")
}

func TestDedupRestartsWhenSourceMoves(t *testing.T) {
	fx := installFake(t)
	dir := t.TempDir()
	const size = 64 << 10
	data := randBytes(t, size)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeSrc(t, a, data)
	writeSrc(t, b, data)
	fx.set(t, a, extent{0, 1 << 30, size, shared})
	fx.set(t, b, extent{0, 1 << 30, size, shared})
	bIno := ino(t, b)
	bMaps := 0
	// After the plan maps b, its extent moves: a write broke the share.
	fx.onMap = func(ino uint64, _ int) {
		if ino != bIno {
			return
		}
		bMaps++
		if bMaps == 2 {
			fx.byIno[ino] = []extent{{0, 2 << 30, size, fiemapExtentLast}}
		}
	}

	o := CopyOptions{Dedup: NewDedup()}
	out := t.TempDir()
	copyOne(t, a, filepath.Join(out, "a"), o)
	copyOne(t, b, filepath.Join(out, "b"), o)
	assert.EqualValues(t, 1, fx.clones.Load(), "b must clone from the plan-time map")
	assert.Zero(t, o.Dedup.Cloned(), "a clone made from a stale map must not count")
}

func TestDedupSkipsDestinationReplacedAfterCommit(t *testing.T) {
	fx := installFake(t)
	dir, out := t.TempDir(), t.TempDir()
	const size = 64 << 10
	data := randBytes(t, size)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeSrc(t, a, data)
	writeSrc(t, b, data)
	fx.set(t, a, extent{0, 1 << 30, size, shared})
	fx.set(t, b, extent{0, 1 << 30, size, shared})

	o := CopyOptions{Dedup: NewDedup()}
	copyOne(t, a, filepath.Join(out, "a"), o)
	// Another process puts different bytes at the path that holds the record.
	repl := filepath.Join(out, "repl")
	writeSrc(t, repl, randBytes(t, size))
	require.NoError(t, os.Rename(repl, filepath.Join(out, "a")))

	copyOne(t, b, filepath.Join(out, "b"), o)
	assert.Zero(t, o.Dedup.Cloned(), "a replaced file is not a clone source")
}

func TestDedupOffWhenDestinationCannotClone(t *testing.T) {
	fx := installFake(t)
	fx.cloneErr = unix.EOPNOTSUPP
	dir := t.TempDir()
	const size = 64 << 10
	data := randBytes(t, size)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeSrc(t, a, data)
	writeSrc(t, b, data)
	fx.set(t, a, extent{0, 1 << 30, size, shared})
	fx.set(t, b, extent{0, 1 << 30, size, shared})

	o := CopyOptions{Dedup: NewDedup()}
	out := t.TempDir()
	copyOne(t, a, filepath.Join(out, "a"), o)
	copyOne(t, b, filepath.Join(out, "b"), o)
	assert.Zero(t, o.Dedup.Cloned())
}

func TestPlanSegments(t *testing.T) {
	exts := []extent{
		{0, 100, 10, 0},                                      // not shared: copied
		{10, 200, 10, fiemapExtentShared},                    // shared
		{30, 300, 10, fiemapExtentShared | 0x8},              // encoded: copied
		{40, 400, 20, fiemapExtentShared | fiemapExtentLast}, // runs past EOF
	}
	got := planSegments(exts, 50)
	assert.Equal(t, []segment{
		{off: 0, length: 10},
		{off: 10, length: 10, phys: 200, shared: true},
		{off: 20, length: 20},
		{off: 40, length: 10, phys: 400, shared: true},
	}, got)
	assert.Equal(t, []segment{{off: 0, length: 5}}, planSegments(nil, 5), "a file with no extents is copied whole")
}

func TestExtentIndexFind(t *testing.T) {
	live, dead := &destRef{}, &destRef{}
	dead.dead.Store(true)
	x := newExtentIndex()
	x.t.ReplaceOrInsert(extentLoc{phys: 100, length: 100, off: 7, ref: live})
	x.t.ReplaceOrInsert(extentLoc{phys: 150, length: 10, ref: live})
	x.t.ReplaceOrInsert(extentLoc{phys: 500, length: 100, ref: dead})

	loc, delta, ok := x.find(170, 20)
	require.True(t, ok, "a short record must not hide a longer one that starts before it")
	assert.EqualValues(t, 100, loc.phys)
	assert.EqualValues(t, 70, delta)

	_, _, ok = x.find(190, 20)
	assert.False(t, ok, "a range past the end of every record is a miss")
	_, _, ok = x.find(510, 10)
	assert.False(t, ok, "a dead record is not a clone source")
}

// TestDedupRealXFS runs the real ioctls: shared extents on one XFS image
// arrive as shared extents on a second XFS image. It needs root, mkfs.xfs
// and loop devices.
func TestDedupRealXFS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to mount loop images")
	}
	if _, err := exec.LookPath("mkfs.xfs"); err != nil {
		t.Skip("needs mkfs.xfs")
	}
	src, dst := mountXFS(t), mountXFS(t)

	const size = 4 << 20
	a, b := filepath.Join(src, "a"), filepath.Join(src, "b")
	writeSrc(t, a, randBytes(t, size))
	fa, err := os.Open(a)
	require.NoError(t, err)
	defer fa.Close()
	fb, err := os.Create(b)
	require.NoError(t, err)
	require.NoError(t, cloneRange(fb, fa, 0, size, 0))
	require.NoError(t, fb.Close())

	o := CopyOptions{Dedup: NewDedup()}
	copyOne(t, a, filepath.Join(dst, "a"), o)
	copyOne(t, b, filepath.Join(dst, "b"), o)
	assert.EqualValues(t, size, o.Dedup.Cloned())

	out, err := os.Open(filepath.Join(dst, "b"))
	require.NoError(t, err)
	defer out.Close()
	exts, err := fiemap(out, 0, size)
	require.NoError(t, err)
	require.NotEmpty(t, exts)
	for _, e := range exts {
		assert.NotZero(t, e.flags&fiemapExtentShared, "the destination copy of b must share its extents with a")
	}
}

func mountXFS(t *testing.T) string {
	img := filepath.Join(t.TempDir(), "fs.img")
	require.NoError(t, os.WriteFile(img, nil, 0o600))
	require.NoError(t, os.Truncate(img, 320<<20))
	if out, err := exec.Command("mkfs.xfs", "-q", "-m", "reflink=1", img).CombinedOutput(); err != nil {
		t.Skipf("mkfs.xfs: %v: %s", err, out)
	}
	mnt := t.TempDir()
	if out, err := exec.Command("mount", "-o", "loop", img, mnt).CombinedOutput(); err != nil {
		t.Skipf("loop mount: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("umount", mnt).Run() })
	return mnt
}
