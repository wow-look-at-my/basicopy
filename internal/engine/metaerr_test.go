package engine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// chtimesFailFS is the local FS with chtimes failing.
type chtimesFailFS struct{ vfs.FS }

func (chtimesFailFS) Chtimes(string, time.Time, time.Time) error { return errors.New("boom") }

// TestMetadataFailureIsWarningNotFailure: a file whose data landed but whose
// metadata did not counts as copied, prints a warning, and is not retried.
func TestMetadataFailureIsWarningNotFailure(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.bin")
	writeFile(t, src, []byte("data"), 0o644)
	info, err := os.Lstat(src)
	require.NoError(t, err)

	var errOut bytes.Buffer
	r := &runner{opts: &options.Options{}, stdout: &bytes.Buffer{}, stderr: &errOut, dfs: chtimesFailFS{vfs.Local}}
	r.copyOpts = fsx.CopyOptions{Preserve: true}

	dst := filepath.Join(root, "dst.bin")
	r.copyOne(fileJob{srcFS: vfs.Local, src: src, dst: dst, info: info})
	assert.Zero(t, r.failed.Load())
	assert.Equal(t, int64(1), r.files.Load())
	assert.Equal(t, int64(4), r.bytes.Load())
	assert.Contains(t, errOut.String(), "basicopy: warning: metadata on "+dst)
	assert.NotContains(t, errOut.String(), "error")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "data", string(got))
}
