// version is overridden at build time via -ldflags "-X main.version=...".

package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the basicopy version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "basicopy %s%s (%s/%s, %s)\n",
			version, buildCommit(debug.ReadBuildInfo), runtime.GOOS, runtime.GOARCH, runtime.Version())
	},
}

// buildCommit names the commit the binary was built from, which the release number alone does not tell.
func buildCommit(read func() (*debug.BuildInfo, bool)) string {
	info, ok := read()
	if !ok {
		return ""
	}
	var rev, at, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if at != "" {
		return fmt.Sprintf(" commit %s%s, %s", rev, dirty, at)
	}
	return fmt.Sprintf(" commit %s%s", rev, dirty)
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
