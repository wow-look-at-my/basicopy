# basicopy — developer notes

An auto-scaling, robocopy-grade local file copier. See `README.md` for the user
story and flags; this file is for working on the code.

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
  engine/    orchestration: pipelined walk (dirs/symlinks/hardlinks on the walk
             goroutine) feeding a worker pool through a resizable gate; plus the
             autoscale loop, watchdog, retry, progress, and mirror passes
  control/   the auto-scaling controller — pure decision logic (no I/O), tested
             against synthetic throughput curves
  device/    HDD/SSD classification, optimal_io_size, same-spindle id, %util
             sampler (Linux via /sys + /proc/diskstats; stubs elsewhere)
  sysload/   system-wide CPU sampler (Linux /proc/stat; stub elsewhere)
  fsx/       copy primitives: crash-safe temp+rename, metadata, and per-OS fast
             paths (Linux reflink/sparse/copy_file_range; buffered elsewhere)
  scan/      skip-unchanged decision (size+mtime, or BLAKE3 with --checksum)
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
  locking); file copies run on the pool. Directory metadata and hardlinks are
  applied after all copies so file writes don't clobber dir mtimes and link
  targets exist.
- **Crash safety:** every file is written to a temp file in the destination
  directory and atomically renamed; an interrupted run never leaves a corrupt
  file at the final path.

## Testing notes

- Time-driven loops (`controlInterval`, `watchInterval`, `progressInterval`,
  retry delays) are package vars so tests can shorten them.
- The env runs as root, so permission-denied paths can't be exercised; tests use
  other real failure modes (missing sources, dest collisions, ENOTDIR).
- `fileKey`/`fileDev` (inode identity, device) are Unix-only; hardlink and
  one-file-system tests live in `*_unix_test.go`.
