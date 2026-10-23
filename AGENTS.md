# basicopy — developer notes

An auto-scaling, robocopy-grade file copier, local or over SSH. See `README.md` for the user story and flags. This file is for working on the code.

## The rule that outranks everything else: no tuning knobs

basicopy exists because `robocopy /MT:n` makes you *guess* a thread count and `rsync` makes you assemble a flag soup. **A tuning flag is a bug report against the controller.** If a setting can be measured, it MUST be measured — at runtime, on the live signal, with no user involvement.

- **Not fully automatic is not an optimization.** A performance option the user has to find, understand, and set correctly is a waste of everyone's time: almost nobody sets it. The people who do mostly set it incorrect, and it exists only because the automatic path was not written. Write the automatic path.
- **"Add a flag for it" is never the answer** to "this is faster in some situations". Find the signal that distinguishes those situations, feed it to the controller, and let the loop decide (throughput plateau is usually it).
- **The existing flags are not precedent.** `--max-threads` and `--buffer` are *ceilings and escape hatches* for shared machines, not tuning dials, and the list does not grow. Behavior flags (`--mirror`, `--checksum`, `--dry-run`) say *what to do*, not *how fast to do it*.
- **No environment variables, no config file, no `-o` passthrough** for performance either — same rule, different spelling.
- Applies to remote copies exactly as it does to local ones. The SSH connection pool widens itself on the plateau signal (`docs/ssh.md`). Everything about *how* to reach a host comes from the user's own `~/.ssh/config`, which they already maintain for every other tool.

## Build & test

Always use `go-toolchain` (no bare `go` commands). Run it with no arguments from the repo root. It handles mod tidy, vet, tests with coverage (80% minimum), and builds the binary to `./build/basicopy`.

```sh
go-toolchain
```

CI is `.github/workflows/ci.yml` (the go-toolchain GitHub Action). Tests use the org testify fork: `github.com/wow-look-at-my/testify/{assert,require}`.

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
             paths (Linux reflink/dedup/sparse/copy_file_range; buffered elsewhere)
  scan/      reason-coded skip-unchanged compare: Compare returns why a copy is
             needed (new/type change/size/mtime/content) or which attributes
             drifted (mode/owner/mtime) on an unchanged file; size+mtime by
             default, BLAKE3 with --checksum
  vfs/       the filesystem seam: local (os pass-through) and remote (SFTP over
             an ssh subprocess), scp-style path parsing, and the self-widening
             SSH connection pool -- docs/ssh.md
```

Platform-specific code is build-tagged (`*_linux.go`, `*_unix.go`, `*_other.go`) behind small functions. `control`, `engine`, and `options` are platform-agnostic.

## Docs

- `docs/ssh.md` -- remote copies: path syntax, why the ssh binary is the transport, connection scaling, and what SFTP cannot carry.

## Key design points

- **The controller is the heart.** `control.Controller` consumes `Sample`s (throughput + optional CPU/util guards) and returns the worker limit `W`. Throughput plateau is the primary signal (it is the only one present for disk, bus/link, and CPU bottlenecks alike), CPU and HDD `%util` only *gate growth*. At the plateau it trims to the minimum `W` that still holds the plateau peak. It is pure so it can be unit-tested deterministically.
- **The gate** (`engine/gate.go`) is a resizable concurrency limiter. The controller calls `setLimit` each tick. A fixed pool of workers acquires it per job, so scaling does not churn goroutines.
- **The walk never waits on the copy.** It feeds the workers through an unbounded queue (`engine/queue.go`). The totals and ETA are last once the walk ends, not when the copy nears its end. While it runs, the progress line marks the totals with `+`.
- **Walk vs workers.** Directory creation, symlink handling, hardlink detection, and exclude/one-file-system decisions happen on the walk goroutine (no locking). File copies — and the per-file skip-unchanged check, so --checksum hashing parallelizes instead of serializing on the walk — run on the pool. Directory metadata and hardlinks are applied after all copies so file writes do not clobber dir mtimes and link targets exist.
- **Crash safety:** every file is written to a temp file in the destination directory and atomically renamed. An interrupted run never leaves a corrupt file at the last path.
- **One code path, kinds of filesystem.** Nothing outside `vfs` calls `os.*` or `filepath.*` on a copy path: each side of a copy carries a `vfs.FS` (which also supplies the path helpers, POSIX for remote). Adding a capability means adding it once.
- **One connection per concurrent transfer.** An sftp-server answers one request at a time, so transfers sharing a connection run in sequence on the far side. `vfs.remoteFS` dials on demand (cap 16, first connection reserved for metadata). Restraint comes from `W`, which is already measured. `docs/ssh.md`.
- **`--move`** (`engine/move.go`): one rename per root when the target name is free on the same host, else copy and record each finished source entry. The removal pass runs after `--mirror`, which reads the source.
- **Shared extents cross once** (`fsx/extents*.go`). When the whole-file reflink fails (another filesystem), FIEMAP finds the source's shared extents. The first copy of each is recorded in a run-wide index the moment its bytes land. Later references clone it with `FICLONERANGE`, and a concurrent worker waits on the claim rather than copy it twice. A clone is trusted only if the source map is unchanged after the copy, else the file is copied again in full.
- **A stall means grow, not stop.** `control.Sample.Blocked` (every worker busy, nothing finished) makes the controller grow `W`. At zero throughput the plateau test "the last increase didn't help" is true at every `W`. Which used to pin cloud-backed (OneDrive placeholder) copies to one file at a time.

## Testing notes

- Time-driven loops (`controlInterval`, `watchInterval`, `progressInterval`, retry delays) are package vars so tests can shorten them.
- `Run`'s per-item/diagnostic output goes through the `runStdout`/`runStderr` seam vars (engine.go) so full-run tests can capture it -- see `captureRun` in `itemize_test.go`. Dry runs itemize on stdout by default, so hand-built `runner` literals in tests need a non-nil `stdout` (e.g. `io.Discard`).
- The env runs as root, so permission-denied paths cannot be exercised. Tests use other real failure modes (missing sources, dest collisions, ENOTDIR).
- `vfs.FS.FileID`/`Dev` (inode identity, device) answer only on a local `unix` build. The shipped APE is one on every host, Windows included, so `dats/metadata.dats` expects hardlinks kept on all three. Go tests of hardlinks and one-file-system live in `*_unix_test.go`.
- Remote tests need no network, credentials, or `ssh` binary. They swap `vfs.StartTransport` for an in-process `pkg/sftp` server over pipes (see `internal/vfs/testserver_test.go`, `internal/engine/remote_test.go`), so the real protocol code runs.
- `dats/data.dats` and `dats/metadata.dats` test the built binary itself. go-toolchain runs them on Linux. CI's `every-host` job runs them on macOS and Windows against the same APE, received with `buildhost-handoff`. `dats/fstree.mjs` builds the fixture and compares trees, in node so one script serves all hosts. A per-host answer starts with the host name.
- `xplat/` is the cross-host pair. `produce.dats` sends each host's copy and manifest, and `receive.dats` copies every host's tree on each host. Both need `--no-sandbox`, so go-toolchain does not run them.
- That server answers requests concurrently, unlike a real sftp-server, so it cannot show serialization. `engine/slowremote_test.go` proxies it into one request at a time with delayed reads. The only faithful model of a cloud-backed remote, and the regression test for parallel transfers.
