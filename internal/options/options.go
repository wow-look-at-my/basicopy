// Package options holds the fully-parsed configuration for a single basicopy run.
// It is shared between the CLI layer (which fills it from flags) and the engine
// (which consumes it), and deliberately has no dependency on cobra so the engine
// stays usable as a library.
package options

import (
	"fmt"
	"strconv"
	"strings"
)

// Options is the complete, validated configuration for a copy run.
type Options struct {
	// Sources is the list of SRC arguments (files and/or directories).
	Sources []string

	// Exactly one of TargetDir / TargetFile is set (enforced by Validate).
	//
	//   TargetDir:  each source lands under DIR as DIR/<basename(src)>.
	//   TargetFile: the single source file is copied to exactly this path.
	TargetDir  string
	TargetFile string

	// NoAutoMkdirs turns a missing target parent directory into an error
	// instead of creating it (and announcing each created dir on stderr).
	NoAutoMkdirs bool

	// Scaling / performance. These are escape hatches; the tool self-tunes.
	MaxThreads int   // optional hard cap on the autoscaler; 0 = auto.
	BufferSize int64 // override the device-adaptive buffer size; 0 = auto.

	// Selection.
	Exclude       []string // glob patterns to skip.
	Include       []string // glob patterns to re-include under an exclude.
	OneFileSystem bool     // don't cross mount points.

	// Behavior.
	DryRun            bool
	Mirror            bool // delete extraneous files in the target (destructive).
	Checksum          bool // compare by BLAKE3 content hash, not size+mtime.
	NoHardlinks       bool // duplicate hardlinked files instead of preserving links.
	NoFollowSymlinks  bool // copy symlinks as links instead of dereferencing them.
	NoSymlinkWarnings bool // suppress the stderr notice for out-of-tree symlinks.
	NoPreserve        bool // don't preserve metadata (mode/times/owner/xattr/Linux ACL).
	FatalErrors       bool // abort on the first error instead of isolate-and-continue.
	Fsync             bool // fsync each file before the atomic rename (durability).

	// Output.
	Verbose  bool
	Quiet    bool
	JSON     bool
	Progress string // auto|always|never
}

// Validate checks the option combination for internal consistency and returns a
// human-readable error describing the first problem found.
func (o *Options) Validate() error {
	if len(o.Sources) == 0 {
		return fmt.Errorf("no SRC given: specify at least one source")
	}

	switch {
	case o.TargetDir == "" && o.TargetFile == "":
		return fmt.Errorf("a destination is required: pass --target-dir DIR or --target-file FILE")
	case o.TargetDir != "" && o.TargetFile != "":
		return fmt.Errorf("--target-dir and --target-file are mutually exclusive")
	}

	if o.TargetFile != "" && len(o.Sources) != 1 {
		return fmt.Errorf("--target-file takes exactly one source file, got %d", len(o.Sources))
	}

	if o.Mirror && o.TargetDir == "" {
		return fmt.Errorf("--mirror only makes sense with --target-dir")
	}

	if o.Verbose && o.Quiet {
		return fmt.Errorf("--verbose and --quiet are mutually exclusive")
	}

	switch o.Progress {
	case "", "auto", "always", "never":
	default:
		return fmt.Errorf("--progress must be auto|always|never, got %q", o.Progress)
	}

	if o.MaxThreads < 0 {
		return fmt.Errorf("--max-threads cannot be negative")
	}
	if o.BufferSize < 0 {
		return fmt.Errorf("--buffer cannot be negative")
	}

	return nil
}

// ParseSize parses a human size string such as "4MiB", "64MB", "512k", or a bare
// byte count. Binary units use the "i" infix (KiB, MiB, GiB, TiB); decimal units
// (KB, MB, GB, TB) are powers of 1000. A bare number is bytes. Empty string -> 0.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	numStr, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	if numStr == "" {
		return 0, fmt.Errorf("invalid size %q: missing number", s)
	}
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %v", s, err)
	}
	if num < 0 {
		return 0, fmt.Errorf("invalid size %q: negative", s)
	}

	var mult float64
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kb":
		mult = 1e3
	case "ki", "kib":
		mult = 1 << 10
	case "m", "mb":
		mult = 1e6
	case "mi", "mib":
		mult = 1 << 20
	case "g", "gb":
		mult = 1e9
	case "gi", "gib":
		mult = 1 << 30
	case "t", "tb":
		mult = 1e12
	case "ti", "tib":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("invalid size unit %q in %q", unit, s)
	}

	return int64(num * mult), nil
}
