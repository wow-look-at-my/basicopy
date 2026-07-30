# basicopy — developer notes

An auto-scaling, robocopy-grade file copier, local or over SSH. See `README.md`
for the user story and flags; this file is for working on the code.

## The rule that outranks everything else: no tuning knobs

basicopy exists because `robocopy /MT:n` makes you *guess* a thread count and
`rsync` makes you assemble a flag soup. **A tuning flag is a bug report against
the controller.** If a setting could be measured, it MUST be measured — at
runtime, on the live signal, with no user involvement.

- **Not fully automatic is not an optimization.** A performance option the user
  has to discover, understand, and set correctly is a waste of everyone's time:
  almost nobody sets it, the people who do mostly set it wrong, and it exists
  only because the automatic path was not written. Write the automatic path.
- **"Add a flag for it" is never the answer** to "this is faster in some
  situations". Find the signal that distinguishes those situations, feed it to
  the controller, and let the loop decide (throughput plateau is usually it).
- **The existing flags are not precedent.** `--max-threads` and `--buffer` are
  *ceilings and escape hatches* for shared machines, not tuning dials, and the
  list does not grow. Behavior flags (`--mirror`, `--checksum`, `--dry-run`) say
  *what to do*, never *how fast to do it*.
- **No environment variables, no config file, no `-o` passthrough** for
  performance either — same rule, different spelling.
- Applies to remote copies exactly as it does to local ones: the SSH connection
  pool widens itself on the plateau signal (`docs/ssh.md`), and everything about
  *how* to reach a host comes from the user's own `~/.ssh/config`, which they
  already maintain for every other tool.

## Build & test

Always use `go-toolchain` (no bare `go` commands). Run it with no arguments from
the repo root — it handles mod tidy, vet, tests with coverage (80% minimum), and
builds the binary to `./build/basicopy`.

```sh
go-toolchain
```

CI is `.github/workflows/ci.yml` (the go-toolchain GitHub Action). Tests use the
org testify fork: `github.com/wow-look-at-my/testify/{assert,require}`.

## Layout

```
cmd/basicopy/        cobra CLI (one command per file, self-registering via init())
internal/
  options/   parsed flags + Validate() + human size parsing (no cobra dependency)
  engine/    orchestration (engine.go) + walking/dispatch/itemized output
             (walk.go): pipelined walk (dirs/symlinks/hardlinks on the walk
             goroutine) feeding a worker pool through a resizable gate; plus the
             autoscale loop, watchdog, retry, progress, and mirror passes
  control/   the auto-scaling controller — pure decision logic (no I/O), tested
             against synthetic throughput curves
  device/    HDD/SSD classification, optimal_io_size, same-spindle id, %util
             sampler (Linux via /sys + /proc/diskstats; stubs elsewhere)
  sysload/   system-wide CPU sampler (Linux /proc/stat; stub elsewhere)
  fsx/       copy primitives: crash-safe temp+rename, metadata, and per-OS fast
             paths (Linux reflink/sparse/copy_file_range; buffered elsewhere)
  scan/      reason-coded skip-unchanged compare: Compare returns why a copy is
             needed (new/type change/size/mtime/content) or which attributes
             drifted (mode/owner/mtime) on an unchanged file; size+mtime by
             default, BLAKE3 with --checksum
```

Platform-specific code is build-tagged (`*_linux.go`, `*_unix.go`, `*_other.go`)
behind small functions; `control`, `engine`, and `options` are platform-agnostic.

## Key design points

- **The controller is the heart.** `control.Controller` consumes `Sample`s
  (throughput + optional CPU/util guards) and returns the worker limit `W`.
  Throughput plateau is the primary signal (it's the only one present for disk,
  bus/link, and CPU bottlenecks alike); CPU and HDD `%util` only *gate growth*.
  At the plateau it trims to the minimum `W` that still holds the plateau peak.
  It is pure so it can be unit-tested deterministically.
- **The gate** (`engine/gate.go`) is a resizable concurrency limiter; the
  controller calls `setLimit` each tick. A fixed pool of workers acquires it per
  job, so scaling doesn't churn goroutines.
- **Walk vs workers.** Directory creation, symlink handling, hardlink detection,
  and exclude/one-file-system decisions happen on the single walk goroutine (no
  locking); file copies — and the per-file skip-unchanged check, so --checksum
  hashing parallelizes instead of serializing on the walk — run on the pool.
  Directory metadata and hardlinks are applied after all copies so file writes
  don't clobber dir mtimes and link targets exist.
- **Crash safety:** every file is written to a temp file in the destination
  directory and atomically renamed; an interrupted run never leaves a corrupt
  file at the final path.

## Testing notes

- Time-driven loops (`controlInterval`, `watchInterval`, `progressInterval`,
  retry delays) are package vars so tests can shorten them.
- `Run`'s per-item/diagnostic output goes through the `runStdout`/`runStderr`
  seam vars (engine.go) so full-run tests can capture it -- see `captureRun` in
  `itemize_test.go`. Dry runs itemize on stdout by default, so hand-built
  `runner` literals in tests need a non-nil `stdout` (e.g. `io.Discard`).
- The env runs as root, so permission-denied paths can't be exercised; tests use
  other real failure modes (missing sources, dest collisions, ENOTDIR).
- `fileKey`/`fileDev` (inode identity, device) are Unix-only; hardlink and
  one-file-system tests live in `*_unix_test.go`.
