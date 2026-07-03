package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/engine"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

// newCmd returns a command with a context and output sink, mirroring how cobra
// sets a context during ExecuteContext (a bare command's Context() is nil, which
// engine.Run would dereference).
func newCmd(out io.Writer) *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.SetOut(out)
	return c
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "0 B", humanBytes(0))
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "1.0 KiB", humanBytes(1024))
	assert.Equal(t, "1.0 MiB", humanBytes(1<<20))
	assert.Equal(t, "1.5 GiB", humanBytes(3<<29))
	assert.Equal(t, "2.0 TiB", humanBytes(2<<40))
}

// mkTree creates a tiny source tree and returns its path plus a (not-yet-created)
// destination path under the same temp root.
func mkTree(t *testing.T) (src, dst string) {
	t.Helper()
	root := t.TempDir()
	src = filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644))
	return src, filepath.Join(root, "dst")
}

func TestRunTextSummary(t *testing.T) {
	src, dst := mkTree(t)
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Progress: "never"}
	require.NoError(t, o.Validate())

	var out bytes.Buffer
	cmd := newCmd(&out)
	require.NoError(t, run(cmd, o))
	assert.Contains(t, out.String(), "1 files")
	assert.Contains(t, out.String(), "0 updated")
}

func TestRunMirrorSummary(t *testing.T) {
	src, dst := mkTree(t)
	o := &options.Options{Sources: []string{src}, TargetDir: dst, Mirror: true, Progress: "never"}
	require.NoError(t, o.Validate())

	var out bytes.Buffer
	cmd := newCmd(&out)
	require.NoError(t, run(cmd, o))
	assert.Contains(t, out.String(), "deleted")
}

func TestRunJSONSummary(t *testing.T) {
	src, dst := mkTree(t)
	o := &options.Options{Sources: []string{src}, TargetDir: dst, JSON: true, Progress: "never"}
	require.NoError(t, o.Validate())

	var out bytes.Buffer
	cmd := newCmd(&out)
	require.NoError(t, run(cmd, o))
	assert.Contains(t, out.String(), `"files": 1`)

	// The whole stdout payload must be one JSON document -- nothing before or
	// after it (machine consumers parse the stream as-is).
	var sum engine.Summary
	require.NoError(t, json.Unmarshal(out.Bytes(), &sum), "stdout must be pure JSON, got: %q", out.String())
	assert.EqualValues(t, 1, sum.Files)
}

func TestRunReportsFailure(t *testing.T) {
	root := t.TempDir()
	o := &options.Options{
		Sources:   []string{filepath.Join(root, "missing")},
		TargetDir: filepath.Join(root, "dst"),
		Progress:  "never",
	}
	require.NoError(t, o.Validate())

	cmd := newCmd(io.Discard)
	assert.Error(t, run(cmd, o), "a failed item must yield a non-nil error (non-zero exit)")
}

func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	versionCmd.SetOut(&out)
	versionCmd.Run(versionCmd, nil)
	assert.Contains(t, out.String(), "basicopy ")
}

func TestRootCommandCopies(t *testing.T) {
	src, dst := mkTree(t)
	opts = options.Options{}
	bufferStr = ""
	rootCmd.SetArgs([]string{src, "--target-dir", dst, "--progress", "never", "--buffer", "1MiB"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	require.NoError(t, rootCmd.Execute())
	_, err := os.Stat(filepath.Join(dst, "src", "a.txt"))
	require.NoError(t, err, "the root command should copy the tree")
}

func TestRootCommandBadBuffer(t *testing.T) {
	opts = options.Options{}
	bufferStr = ""
	rootCmd.SetArgs([]string{"x", "--target-dir", "y", "--buffer", "not-a-size"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	assert.Error(t, rootCmd.Execute(), "an unparseable --buffer must error")
}

func TestRootCommandMissingTarget(t *testing.T) {
	opts = options.Options{}
	bufferStr = ""
	rootCmd.SetArgs([]string{"x"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	assert.Error(t, rootCmd.Execute(), "no destination must fail validation")
}
