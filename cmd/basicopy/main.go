// Command basicopy is an auto-scaling, robocopy-grade local file copier.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wow-look-at-my/basicopy/internal/options"
)

var (
	opts      options.Options
	bufferStr string
)

var rootCmd = &cobra.Command{
	Use:   "basicopy [flags] SRC...",
	Short: "Auto-scaling, robocopy-grade file copier",
	Long: `basicopy copies files and directory trees as fast as the hardware allows,
auto-scaling its own parallelism to saturate the slowest link in the chain
(disk, bus, or connection) without bogging the system down.

The destination is always explicit:
  basicopy SRC...  --target-dir DIR     copy each SRC under DIR as DIR/<basename>
  basicopy SRC     --target-file FILE   copy a single SRC file to exactly FILE`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts.Sources = args
		if bufferStr != "" {
			n, err := options.ParseSize(bufferStr)
			if err != nil {
				return err
			}
			opts.BufferSize = n
		}
		if err := opts.Validate(); err != nil {
			return err
		}
		return run(cmd, &opts)
	},
}

func init() {
	f := rootCmd.Flags()

	// Destination.
	f.StringVar(&opts.TargetDir, "target-dir", "", "copy each SRC under this directory as DIR/<basename>")
	f.StringVar(&opts.TargetFile, "target-file", "", "copy a single SRC file to exactly this path")
	f.BoolVar(&opts.Contents, "contents", false, "copy each source directory's contents into --target-dir itself instead of nesting under the source's basename (rsync SRC/ trailing-slash equivalent)")
	f.BoolVar(&opts.NoAutoMkdirs, "no-auto-mkdirs", false, "error on missing target parents instead of creating them")

	// Scaling / performance (the tool self-tunes; these are escape hatches).
	f.IntVar(&opts.MaxThreads, "max-threads", 0, "optional hard cap on the autoscaler (0 = auto)")
	f.StringVar(&bufferStr, "buffer", "", "override the device-adaptive buffer size (e.g. 4MiB)")

	// Selection.
	f.StringArrayVar(&opts.Exclude, "exclude", nil, "skip paths matching this glob (repeatable)")
	f.StringArrayVar(&opts.Include, "include", nil, "re-include paths matching this glob under an --exclude (repeatable)")
	f.BoolVar(&opts.OneFileSystem, "one-file-system", false, "do not cross mount points")

	// Behavior.
	f.BoolVar(&opts.DryRun, "dry-run", false, "plan only; copy nothing")
	f.BoolVar(&opts.Mirror, "mirror", false, "delete files in the target not present in SRC (destructive)")
	f.BoolVarP(&opts.Checksum, "checksum", "c", false, "compare by BLAKE3 content hash, not size+mtime")
	f.BoolVar(&opts.NoHardlinks, "no-hardlinks", false, "duplicate hardlinked files instead of preserving links")
	f.BoolVar(&opts.NoFollowSymlinks, "no-follow-symlinks", false, "copy symlinks as links instead of dereferencing them")
	f.BoolVar(&opts.NoSymlinkWarnings, "no-symlink-warnings", false, "suppress the stderr notice for out-of-tree symlinks")
	f.BoolVar(&opts.NoPreserve, "no-preserve", false, "do not preserve metadata (mode/times/owner/xattr/Linux ACL)")
	f.BoolVar(&opts.FatalErrors, "fatal-errors", false, "abort on the first error instead of isolate-and-continue")
	f.BoolVar(&opts.Fsync, "fsync", false, "fsync each file before the atomic rename (durability)")

	// Output.
	f.BoolVarP(&opts.Verbose, "verbose", "v", false, "print each copied path")
	f.BoolVarP(&opts.Quiet, "quiet", "q", false, "suppress progress and non-error output")
	f.BoolVar(&opts.JSON, "json", false, "machine-readable JSON progress and summary")
	f.StringVar(&opts.Progress, "progress", "auto", "progress display: auto|always|never")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "basicopy:", err)
		os.Exit(1)
	}
}
