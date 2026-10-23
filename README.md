# basicopy

An auto-scaling, robocopy-grade file copier — local disks or over SSH.

`rsync` is single-threaded, fragile, and version-sensitive. `robocopy` is robust and multithreaded but Windows-only and makes you *guess* a thread count (`/MT:n`). `rclone` is overkill for local copies. **basicopy** is the missing tool. It copies a tree as fast as the hardware allows. That tree tuns its own parallelism automatically** to saturate the slowest link in the chain — disk, bus, or connection — without bogging the system down.

CPU and RAM are explicitly *not* what it tries to saturate.

```
basicopy SRC...  --target-dir DIR      # copy each SRC under DIR as DIR/<basename>
basicopy SRC     --target-file FILE     # copy a single SRC file to exactly FILE
```

Either end may be a remote host, written the way `scp` writes it — no extra flags, no daemon, nothing to install on the far side:

```
basicopy ~/photos --target-dir myotherpc:/home/mhayine/backup
basicopy myotherpc:/home/mhayine/whatever --target-dir ./local
```

## Why it's different

- **Self-tuning, no thread knob.** A closed-loop controller measures achieved throughput every ~250 ms and adjusts the number of concurrent copy streams. It climbs while throughput rises and converges to the *minimum* parallelism that sustains the plateau. So a saturated USB bus or gigabit link is not served by dozens of pointless streams.
- **Plateau is the universal signal.** Disk-bound, bus/link-bound, or CPU-bound (heavy on-access antivirus/EDR) all look the same to the controller: adding workers stops raising throughput. It backs off on rising I/O latency, a pinned system CPU, or a saturated HDD, so the machine stays responsive.
- **Remote copies are just copies.** `host:/path` on either side runs over SSH (SFTP). Every feature comes along: incremental skip-unchanged re-runs, `--checksum`, `--mirror`, `--dry-run` itemization, the space guard, crash-safe publication. It shells out to your own `ssh`, so `~/.ssh/config`, agents, `ProxyJump`, and host keys work exactly as they already do. There is not one SSH option to configure. Connections follow the work. An sftp-server answers one request at a time. Basicopy gives each concurrently-transferred file its own connection (dialed on demand, capped at 16) instead of queuing them behind each other. That is what keeps files that are slow to *open* — OneDrive/Dropbox placeholders downloaded on first read, cold storage — from arriving strictly one. This holds at a time. See [docs/ssh.md](docs/ssh.md).
- **Device-aware.** It classifies the source and destination as SSD or HDD (Linux: `/sys/.../queue/rotational`, `/proc/diskstats`) and seeds concurrency accordingly — conservative on spinning disks (which hate seek-thrash), generous on SSD/NVMe (which need queue depth to fill).
- **Fast copy primitives.** On Linux it prefers. That Linux is in order: a whole-file reflink clone (`FICLONE`, instant on btrfs/XFS-reflink), a sparse-aware copy that preserves holes (`SEEK_DATA`/`SEEK_HOLE`), `copy_file_range` (in-kernel / NFS server-side copy), then a buffered fallback. Other platforms use the buffered path.
- **Robust like robocopy.** Per-file error isolation (one bad file never aborts the run), transient-error retries with backoff, crash-safe writes (temp file + atomic rename — a kill never leaves a corrupt file at the last path). A no-progress watchdog (aborts a stalled non-interactive run after 30 s. Prompts on a TTY). Aborts (Ctrl-C, the watchdog, a full destination) take effect mid-file: in-flight copies stop at the next chunk instead of finishing a multi-gigabyte file first. Leave no partial destination files behind.

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
| Attr touch-up on unchanged entries (mode/owner; +mtime with `--checksum`) | on | `--no-preserve` |
| Dry-run itemizes every pending change (with reason) | on | `--quiet` / `--json` |
| Hardlinks preserved | on | `--no-hardlinks` |
| Follow symlinks (deref in-tree, keep out-of-tree as links) | on | `--no-follow-symlinks` |
| Warn on out-of-tree symlinks | on (stderr) | `--no-symlink-warnings` |
| Error handling | isolate + continue, non-zero exit | `--fatal-errors` |
| Mirror / delete extraneous | off (destructive) | `--mirror` |
| Move (remove the source) | off | `--move` |
| fsync each file | off | `--fsync` |

Short flags exist only where they match the long flag's first letter: `-c` (`--checksum`), `-v` (`--verbose`), `-q` (`--quiet`).

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

# Push a tree to another machine, then mirror it there (host aliases from
# ~/.ssh/config work, as does user@host, [ipv6], and ssh://host:port/path).
basicopy ~/project --target-dir myotherpc:/srv/project
basicopy ~/project --target-dir myotherpc:/srv/project --mirror

# Pull one back.
basicopy myotherpc:/srv/project --target-dir ~/restore

# Move instead of copy: one rename on the same filesystem, else copy then
# remove each source entry that reached the target.
basicopy ~/Downloads/iso --target-dir /mnt/archive --move

# Cap parallelism on a shared box; exclude build junk. A pattern ending in '/'
# matches directories only ('node_modules/' never hits a file of that name).
basicopy repo --target-dir /srv/repo --max-threads 8 --exclude '*.o' --exclude 'node_modules/'

# Preview exactly what a sync would do, verified by content hash (rsync
# --dry-run --itemize-changes --checksum -rlptgoD --exclude 'node_modules/' SRC/ DEST/):
basicopy SRC --contents --target-dir DEST --dry-run --checksum --no-follow-symlinks --exclude 'node_modules/'
```

### Dry runs itemize by default

`--dry-run` prints one line per pending change on stdout, each with its reason -- no extra flag needed (rsync's bare `--dry-run` printing nothing is the trap this avoids). `--quiet` silences it, and so does `--json`, which keeps stdout pure machine-readable JSON (the summary object is the only thing printed there). A real run prints the same lines (minus the "would ") under `--verbose`:

```
would copy DEST/a.txt (new)                  # or: (size 6 -> 27), (mtime differs),
would copy DEST/b.txt (content differs)      #     (type change); content differs
would update DEST/c.txt (mode 0600 -> 0644)  #     needs --checksum
would update DEST/d.txt (owner 1000:1000 -> 0:0, mtime)
would mkdir DEST/sub
would link DEST/l.lnk -> target.txt
would hardlink DEST/h1.bin => DEST/h0.bin
would delete DEST/stale.txt                  # --mirror only
```

An unchanged file whose mode/owner drifted (or, with `--checksum`, whose mtime drifted while the content is verifiably same) is repaired in place instead of staying incorrect forever, and counted as "updated" in the summary. Unchanged entries stay silent unless `--verbose` (`skip unchanged ...`).

### Moving

`--move` renames a source root in one step when the source and target share a filesystem and the target name is free. Otherwise it copies, then removes each source entry that is at the target, after `--mirror` finishes. An interrupted move can be re-run: files already at the target count as unchanged, and their sources are removed.

- Symlinks move as links, as `mv` moves them.
- Excluded entries stay, and so do the directories above them.
- `--contents` moves what is inside a source directory and leaves the directory.
- basicopy never removes a source that is the target itself (a target reached through a symlink to the source).

### Remote paths

`[user@]host:/path` is remote, `[user@]host:path` is relative to the login directory, and `ssh://[user@]host[:port]/path` is the spelling that carries a port. A colon before the first `/` is what makes an argument remote (the scp rule). Write `./weird:name` for a local path that contains one. Sources may name several hosts in one run, and remote-to-remote copies relay through this machine.

What SFTP cannot carry is documented in [docs/ssh.md](docs/ssh.md). SFTP hardlinks in a *remote source* cannot be detected (no inode identity). `--one-file-system` cannot see remote mount points, xattrs/ACLs are not copied when either end is remote, and mtimes are whole seconds. Content, mode, owner, times, symlinks, and hardlink creation on the destination all work.

### Path semantics (no games)

The destination is always explicit — there is no positional `DST` and no trailing-slash magic. With `--target-dir DIR`, each `SRC` lands under `DIR` by its basename (`DIR/<basename(src)>`). Add `--contents` to copy each source *directory's* contents into `DIR` itself instead. Rsync's `SRC/` trailing-slash behavior as an explicit flag, so a path never changes meaning because of a slash (file sources still land under their basename, and `--mirror --contents` takes exactly one source). With `--target-file FILE`, a single source file is copied to exactly `FILE`. Missing parent directories are created and each one is announced on stderr.

## Platform support

- **Any host you can `ssh` into:** the remote end needs only a working SFTP subsystem — i.e. a stock `sshd`. Nothing is installed on the far side.
- **Linux:** full depth — device classification + `%util` guard, reflink, sparse, `copy_file_range`, hardlink, ownership, xattr, and POSIX ACL preservation.
- **macOS / other Unix:** portable buffered copy with metadata/ownership/hardlink preservation. This also covers xattrs are preserved on macOS. The controller runs on throughput
  + latency + system-CPU signals (no `%util`).
- **Windows:** builds with the portable buffered path. This also covers native fast-copy and device classification are not yet implemented.

## Not yet implemented

- Intra-file chunking (multiple parallel streams within one large file) for high-latency network targets.
- Delta transfer (rsync's rolling-checksum block reuse): a changed file is sent whole rather than by difference.
- Native fast-copy primitives on macOS (`clonefile`) and Windows (`CopyFileEx`, ReFS block clone) and their device classification.
- `O_DIRECT` (page-cache-bypassing) reads/writes — needs aligned-buffer handling before it is worth a flag — and `posix_fadvise`/`sync_file_range` page-cache hints for the buffered path.
- Native ACL preservation outside Linux.
- Live JSON progress events (only the last `--json` summary is emitted today).
- Device and special files (mknod): skipped with a stderr note.

See `internal/` for the implementation: `engine` (orchestration + workers), `control` (the auto-scaling controller), `device`/`sysload` (signal sources), `fsx` (copy primitives), `scan` (reason-coded skip-unchanged compare), `vfs` (local and SSH/SFTP filesystems). `walk`-style traversal lives in `engine`.
