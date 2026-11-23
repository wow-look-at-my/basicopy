package engine

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/tml"
	"github.com/wow-look-at-my/tml/cell"
)

// The progress theme spells its colours as theme colours, so the region loads in either mode. Each
// colour keeps the value the display drew with before: the names and the values are unchanged.
func TestProgressThemeKeepsItsColoursInBothModes(t *testing.T) {
	sub, err := fs.Sub(progressUI, "ui/progress")
	require.NoError(t, err)

	want := map[string][2]string{
		"accent": {"#1e66f5", "#7aa2f7"},
		"ok":     {"#40a02b", "#9ece6a"},
		"bad":    {"#d20f39", "#f7768e"},
		"warn":   {"#df8e1d", "#e0af68"},
		"dim":    {"#6c6f85", "#565f89"},
		"track":  {"#ccd0da", "#3b4261"},
	}
	for _, dark := range []bool{false, true} {
		view, err := tml.Load(sub, "Progress.tml", tml.Options{Dark: dark})
		require.NoError(t, err, "the progress region loads with dark=%v", dark)

		for name, modes := range want {
			value := modes[0]
			if dark {
				value = modes[1]
			}
			color, err := view.Theme().Named(name)
			require.NoError(t, err, "the progress theme declares %q", name)
			assert.Equal(t, cell.MustColor(value), color, "%s in dark=%v", name, dark)
		}
	}
}
