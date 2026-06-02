package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wow-look-at-my/basicopy/internal/engine"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

// run drives a copy from validated options and reports a one-line summary.
func run(cmd *cobra.Command, o *options.Options) error {
	sum, err := engine.Run(cmd.Context(), o)
	if err != nil {
		return err
	}
	if !o.Quiet {
		fmt.Fprintf(cmd.OutOrStdout(),
			"basicopy: %d files, %d dirs, %d symlinks, %s; %d skipped, %d failed\n",
			sum.Files, sum.Dirs, sum.Symlinks, humanBytes(sum.Bytes), sum.Skipped, sum.Failed)
	}
	if sum.Failed > 0 {
		return fmt.Errorf("%d item(s) failed", sum.Failed)
	}
	return nil
}

// humanBytes formats a byte count with a binary unit suffix.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
