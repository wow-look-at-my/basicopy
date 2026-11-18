//go:build unix

package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/askpass"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

// scriptPerson makes the person reachable through an askpass program that answers "hunter2".
func scriptPerson(t *testing.T) *askpass.Person {
	t.Helper()
	program := filepath.Join(t.TempDir(), "askpass")
	require.NoError(t, os.WriteFile(program, []byte("#!/bin/sh\necho hunter2\n"), 0o755))
	t.Setenv("SSH_ASKPASS", program)
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	person, ok := askpass.FindPerson()
	require.True(t, ok)
	return person
}

// TestPrompterKeepsTheRegionOffTheQuestion: a question takes the progress
// region off the terminal, and a region comes back after the answer.
func TestPrompterKeepsTheRegionOffTheQuestion(t *testing.T) {
	t.Serial("the askpass environment and the region inspector belong to the process")
	stderr := &syncBuffer{}
	r := &runner{opts: &options.Options{Progress: "always"}, stdout: io.Discard, stderr: stderr, startedAt: time.Now()}
	stop := startRegion(t, r)
	defer stop()
	before := r.display
	p := &prompter{r: r, ctx: context.Background(), person: scriptPerson(t)}

	answer, ok := p.Ask("Enter passphrase for key 'k': ", false)
	require.True(t, ok)
	assert.Equal(t, "hunter2", answer)
	r.outMu.Lock()
	after := r.display
	r.outMu.Unlock()
	require.NotNil(t, after, "the region returns after the answer")
	assert.NotSame(t, before, after, "the old region left the screen for the question")
}

// TestPrompterShowsATouchRequestUnderQuiet: the copy waits on the person, so
// --quiet must not hide the request.
func TestPrompterShowsATouchRequestUnderQuiet(t *testing.T) {
	t.Serial()
	var stderr bytes.Buffer
	r := &runner{opts: &options.Options{Quiet: true}, stdout: io.Discard, stderr: &stderr}
	p := &prompter{r: r, ctx: context.Background(), person: scriptPerson(t)}

	p.Notify("pc", false, "Confirm user presence for key ED25519-SK SHA256:abc")
	p.Notify("pc", true, "Confirm user presence for key ED25519-SK SHA256:abc")
	assert.Equal(t,
		"basicopy: ssh pc: Confirm user presence for key ED25519-SK SHA256:abc\n"+
			"basicopy: ssh pc (another connection): Confirm user presence for key ED25519-SK SHA256:abc\n",
		stderr.String())
}

// TestRemoteRunGivesTheRegistryAPerson: a remote copy hands the person to the
// connections, and a local copy does not look for one.
func TestRemoteRunGivesTheRegistryAPerson(t *testing.T) {
	t.Serial()
	fakeRemote(t)
	person := scriptPerson(t)
	looked := 0
	old := findPerson
	findPerson = func() (*askpass.Person, bool) { looked++; return person, true }
	t.Cleanup(func() { findPerson = old })

	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)

	local := &options.Options{Sources: []string{src}, TargetDir: filepath.Join(root, "local"), Quiet: true}
	require.NoError(t, local.Validate())
	_, err := Run(context.Background(), local)
	require.NoError(t, err)
	assert.Zero(t, looked, "a local copy has no ssh to answer")

	remote := &options.Options{Sources: []string{src}, TargetDir: "pc:" + filepath.Join(root, "remote"), Quiet: true}
	require.NoError(t, remote.Validate())
	sum, err := Run(context.Background(), remote)
	require.NoError(t, err)
	assert.EqualValues(t, 1, sum.Files)
	assert.Equal(t, 1, looked)
}
