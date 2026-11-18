package askpass

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookup(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// TestFindPersonFollowsTheRulesOfSSH: the copy asks where ssh itself would
// ask, given the person's own environment.
func TestFindPersonFollowsTheRulesOfSSH(t *testing.T) {
	const gui = "/usr/bin/ksshaskpass"
	cases := []struct {
		name    string
		vars    map[string]string
		tty     bool
		program string // "" with ok means the terminal
		ok      bool
	}{
		{name: "a terminal and no settings", tty: true, ok: true},
		{name: "no terminal and no program", ok: false},
		{name: "a program is not used while a terminal exists", vars: map[string]string{"SSH_ASKPASS": gui, "DISPLAY": ":0"}, tty: true, ok: true},
		{name: "no terminal, a program and a display", vars: map[string]string{"SSH_ASKPASS": gui, "DISPLAY": ":0"}, program: gui, ok: true},
		{name: "wayland counts as a display", vars: map[string]string{"SSH_ASKPASS": gui, "WAYLAND_DISPLAY": "wayland-0"}, program: gui, ok: true},
		{name: "no terminal and no display", vars: map[string]string{"SSH_ASKPASS": gui}, ok: false},
		{name: "force wins over a terminal", vars: map[string]string{"SSH_ASKPASS": gui, "SSH_ASKPASS_REQUIRE": "force"}, tty: true, program: gui, ok: true},
		{name: "prefer needs a display", vars: map[string]string{"SSH_ASKPASS": gui, "SSH_ASKPASS_REQUIRE": "prefer"}, tty: true, ok: true},
		{name: "prefer with a display", vars: map[string]string{"SSH_ASKPASS": gui, "SSH_ASKPASS_REQUIRE": "Prefer", "DISPLAY": ":0"}, tty: true, program: gui, ok: true},
		{name: "never forbids the program", vars: map[string]string{"SSH_ASKPASS": gui, "SSH_ASKPASS_REQUIRE": "never", "DISPLAY": ":0"}, ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := findPerson(lookup(c.vars), c.tty)
			require.Equal(t, c.ok, ok)
			if ok {
				assert.Equal(t, c.program, p.program)
			}
		})
	}
}

func TestLookSelf(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

	got, err := lookSelf(bin)
	require.NoError(t, err)
	assert.Equal(t, bin, got)

	// A bare name is found on PATH, as the shell found it.
	t.Setenv("PATH", dir)
	got, err = lookSelf("tool")
	require.NoError(t, err)
	assert.Equal(t, bin, got)

	_, err = lookSelf("")
	assert.Error(t, err)
	_, err = lookSelf("no-such-tool-anywhere")
	assert.Error(t, err)
	_, err = lookSelf(dir)
	assert.Error(t, err, "a directory is not an executable")
	_, err = lookSelf(filepath.Join(dir, "missing"))
	assert.Error(t, err)

	self, err := selfPath()
	require.NoError(t, err)
	assert.FileExists(t, self)
}

func TestAskAfterCancelAsksNobody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := (&Person{}).Ask(ctx, "Password: ", false)
	assert.False(t, ok)
}
