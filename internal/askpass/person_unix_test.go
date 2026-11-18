//go:build unix

package askpass

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAskThroughThePersonsOwnProgram: with no terminal, the question goes to
// the askpass program that the person's environment names, as ssh would send it.
func TestAskThroughThePersonsOwnProgram(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "askpass")
	seen := filepath.Join(dir, "seen")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > '" + seen + "'\necho 'hunter2 pass'\necho 'a second line that ssh would not read'\n"
	require.NoError(t, os.WriteFile(program, []byte(script), 0o755))

	p, ok := findPerson(lookup(map[string]string{"SSH_ASKPASS": program, "DISPLAY": ":0"}), false)
	require.True(t, ok)
	answer, ok := p.Ask(context.Background(), keyPrompt, false)
	require.True(t, ok)
	assert.Equal(t, "hunter2 pass", answer)
	shown, err := os.ReadFile(seen)
	require.NoError(t, err)
	assert.Equal(t, keyPrompt, string(shown))

	// A cancel in the program's own dialog is a failed exit: nobody answered.
	require.NoError(t, os.WriteFile(program, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	_, ok = p.Ask(context.Background(), keyPrompt, false)
	assert.False(t, ok)
}
