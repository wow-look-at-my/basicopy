# basicopy

An auto-scaling, robocopy-grade local file copier.

`rsync` is single-threaded, fragile, and version-sensitive. `robocopy` is robust
and multithreaded but Windows-only and makes you *guess* a thread count
(`/MT:n`). `rclone` is overkill for local copies. **basicopy** is the missing
tool: it copies a tree as fast as the hardware allows, **tuning its own
parallelism automatically** to saturate the slowest link in the chain — disk,
bus, or connection — without bogging the system down. CPU and RAM are explicitly
*not* what it tries to saturate.

```
basicopy SRC...  --target-dir DIR      # copy each SRC under DIR as DIR/<basename>
basicopy SRC     --target-file FILE     # copy a single SRC file to exactly FILE
```

## Why it's different

- **Self-tuning, no thread knob.** A closed-loop controller measures achieved
  throughput every ~250 ms and adjusts the number of concurrent copy streams.
  It climbs while throughput rises and converges to the *minimum* parallelism
  that sustains the plateau — so a saturated USB bus or gigabit link isn't served
  by dozens of pointless streams.
- **Plateau is the universal signal.** Disk-bound, bus/link-bound, or CPU-bound
  (heavy on-access antivirus/EDR) all look the same to the controller: adding
  workers stops raising throughput. It backs off on rising I/O latency, a pinned
  system CPU, or a saturated HDD, so the machine stays responsive.
- **Device-aware.** It classifies the source and destination as SSD or HDD
  (Linux: `/sys/.../queue/rotational`, `/proc/diskstats`) and seeds concurrency
  accordingly — conservative on spinning disks (which hate seek-thrash), generous
  on SSD/NVMe (which need queue depth to fill).
- **Fast copy primitives.** On Linux it prefers, in order: a whole-file reflink
  clone (`FICLONE`, instant on btrfs/XFS-reflink), a sparse-aware copy that
  preserves holes (`SEEK_DATA`/`SEEK_HOLE`), `copy_file_range` (in-kernel / NFS
  server-side copy), then a buffered fallback. Other platforms use the buffered
  path.
- **Robust like robocopy.** Per-file error isolation (one bad file never aborts
  the run), transient-error retries with backoff, crash-safe writes (temp file +
  atomic rename — a kill never leaves a corrupt file at the final path), and a
  no-progress watchdog (aborts a stalled non-interactive run after 30 s; prompts
  on a TTY). Aborts (Ctrl-C, the watchdog, a full destination) take effect
  mid-file: in-flight copies stop at the next chunk instead of finishing a
  multi-gigabyte file first, and leave no partial destination files behind.

## Install / build

The project builds with [`go-toolchain`](https://github.com/wow-look-at-my/go-toolchain):

```sh
go-toolchain            # tidy, test, build -> ./build/basicopy
```

## Defaults (no footguns, nothing good left off)

| Behavior | Default | Override |
|---|---|---|
| Recursion | on | — |
| Auto-scaling | always on | `--max-threads N` caps the ceiling |
| Reflink / clone when possible | on | — |
| Crash-safe temp + atomic rename | on | — |
| Auto-create target dirs (stderr notice) | on | `--no-auto-mkdirs` |
| Metadata preserve (mode/times/owner/xattr/Linux ACL) | on | `--no-preserve` |
| Sparse / hole-skip | on | — |
| Skip unchanged (size+mtime) | on | `--checksum` for content (BLAKE3) |
| Hardlinks preserved | on | `--no-hardlinks` |
| Follow symlinks (deref in-tree, keep out-of-tree as links) | on | `--no-follow-symlinks` |
| Warn on out-of-tree symlinks | on (stderr) | `--no-symlink-warnings` |
| Error handling | isolate + continue, non-zero exit | `--fatal-errors` |
| Mirror / delete extraneous | off (destructive) | `--mirror` |
| fsync each file | off | `--fsync` |

Short flags exist only where they match the long flag's first letter: `-c`
(`--checksum`), `-v` (`--verbose`), `-q` (`--quiet`).

## Usage

```sh
# Copy two trees and a file into a directory (created if missing).
basicopy ~/photos ~/music ~/notes.txt --target-dir /mnt/backup

# Mirror a tree (delete anything in the destination not in the source).
basicopy ~/project --target-dir /mnt/backup/project --mirror

# Verify by content hash instead of size+mtime.
basicopy data --target-dir /mnt/archive --checksum

# Machine-readable summary.
basicopy data --target-dir /mnt/archive --json

# Cap parallelism on a shared box; exclude build junk.
basicopy repo --target-dir /srv/repo --max-threads 8 --exclude '*.o' --exclude '*.tmp'
```

### Path semantics (no games)

The destination is always explicit — there is no positional `DST` and no
trailing-slash magic. With `--target-dir DIR`, each `SRC` lands under `DIR` by its
basename (`DIR/<basename(src)>`). With `--target-file FILE`, a single source file
is copied to exactly `FILE`. Missing parent directories are created and each one
is announced on stderr.

## Platform support

- **Linux:** full depth — device classification + `%util` guard, reflink, sparse,
  `copy_file_range`, `posix_fadvise`/`sync_file_range`, hardlink, ownership,
  xattr, and POSIX ACL preservation.
- **macOS / other Unix:** portable buffered copy with metadata/ownership/hardlink
  preservation; xattrs are preserved on macOS. The controller runs on throughput
  + latency + system-CPU signals (no `%util`).
- **Windows:** builds with the portable buffered path; native fast-copy and
  device classification are not yet implemented.

## Not yet implemented

- Intra-file chunking (multiple parallel streams within one large file) for
  high-latency network targets.
- Native fast-copy primitives on macOS (`clonefile`) and Windows (`CopyFileEx`,
  ReFS block clone) and their device classification.
- `O_DIRECT` (page-cache-bypassing) reads/writes — needs aligned-buffer handling
  before it's worth a flag.
- Native ACL preservation outside Linux.
- Live JSON progress events (only the final `--json` summary is emitted today).

See `internal/` for the implementation: `engine` (orchestration + workers),
`control` (the auto-scaling controller), `device`/`sysload` (signal sources),
`fsx` (copy primitives), `scan` (skip-unchanged), `walk`-style traversal lives in
`engine`.
