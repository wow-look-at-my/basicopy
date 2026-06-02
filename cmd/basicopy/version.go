package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the basicopy version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "basicopy %s (%s/%s, %s)\n",
			version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
