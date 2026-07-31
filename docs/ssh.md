# Copying over SSH

`basicopy` treats a remote host like any other filesystem. Either end of a copy
may be written the way `scp` and `rsync` write it:

```sh
basicopy ~/photos --target-dir myotherpc:/home/mhayine/backup
basicopy myotherpc:/home/mhayine/whatever --target-dir ./local
basicopy notes.txt --target-file me@myotherpc:/srv/notes.txt
```

Every feature works the same on both sides: the auto-scaling worker pool, the
skip-unchanged incremental pass, `--checksum`, `--mirror`, `--dry-run`
itemization, `--exclude`, the pre-write space guard, the stall watchdog, and
crash-safe temp-file-plus-rename publication.

## Path syntax

| Form | Meaning |
|---|---|
| `host:/abs/path` | absolute path on `host` |
| `host:path` | relative to the login directory on `host` |
| `host:` | the login directory itself |
| `user@host:/path` | log in as `user` |
| `[2001:db8::1]:/path` | bracketed IPv6 literal |
| `ssh://[user@]host[:port]/path` | the only form that can carry a port |
| `./weird:name`, `/abs/weird:name` | forced local, for a local path containing a colon |

A colon *before* the first `/` makes an argument remote — the same rule `scp`
uses, so muscle memory transfers. On Windows a single-letter prefix stays a
drive letter (`C:\Users`), not a host.

Sources may name several different hosts in one run; each gets its own
connection. A copy with a remote source *and* a remote destination works and
relays the bytes through this machine (SFTP has no server-to-server transfer).

## The transport: OpenSSH, not a reimplementation

basicopy runs `ssh -s <host> sftp` and speaks SFTP over the subprocess's stdio,
the same approach rsync takes. That single decision is why there are no
connection settings to learn:

- `~/.ssh/config` applies in full — `Host` aliases, `HostName`, `Port`, `User`,
  `IdentityFile`, `ProxyJump`, `ControlMaster`, `Compression`, everything.
- The agent, hardware keys, `known_hosts`, and password/2FA prompts all behave
  exactly as they do for `ssh` itself. Prompts appear on the terminal because
  they come from the real `ssh`.
- Any host you can `ssh` into, you can `basicopy` to. Any host you cannot, the
  error you get is OpenSSH's own.

There is deliberately **no** `--ssh`, `-e`, `-o`, `-i`, `-p`, or environment
override. Those would only duplicate configuration the user already maintains
for every other SSH tool, and each one is a way to get a copy wrong.

## Connection scaling (automatic, like everything else)

A copy starts with one connection and deep request pipelining: up to 64 SFTP
requests of 32 KiB in flight per file, so the link's round-trip time is hidden
instead of paid once per block. For the large majority of copies that is the
whole story, and one connection is what the run uses start to finish.

One connection cannot do everything, though. It is a single TCP flow (one
congestion window; head-of-line blocking on a lossy long-haul link), encrypted
by a single core (OpenSSH does cipher and MAC work on one thread), and served by
a single `sftp-server` process on the far end that handles its requests in one
serial loop. When any of those is the limit, more streams over the same
connection cannot help — but another connection can.

basicopy detects that condition instead of asking about it. A second controller
— the same plateau-seeking loop that sizes the worker pool, `control.Controller`
— watches throughput once per second and may widen the pool, but only while:

1. **every worker slot is busy.** With idle slots the copy is short of work, not
   short of transport, and another connection would change nothing.
2. **the worker controller has found its plateau.** During its initial climb
   every throughput change is the worker loop's own doing, and reading those
   swings as evidence about connections would have the two loops chasing each
   other.

Then it behaves exactly like the worker loop: add a connection, and keep it only
if the plateau rises. If throughput does not improve, the extra connection stops
receiving work. Growth stops at `vfs.MaxConnections` (8); past a handful the
handshakes cost more than the parallelism returns.

Measured on this repo's test box (loopback SSH, 4 cores, 1.1 GiB in 60 files):
13.0 s on one connection, 8.3 s once the loop widened to two — with no flags on
either run.

Two details make widening safe rather than clever:

- **Extra connections run with `BatchMode=yes`.** A host that needs an
  interactive credential can never be asked for it a second time in the middle
  of a copy. The dial fails immediately, the pool freezes at its current size,
  and the run continues on the connection it already has.
- **Widening never blocks a copy.** The handshake happens on its own goroutine;
  workers keep using the existing connections until the new one is ready.

Narrowing does not close connections. Closing one mid-run would race in-flight
file handles for no measurable gain, so the extras simply stop carrying work.

## What SFTP cannot carry

These are protocol limits, not omissions. Everything else — content, mode,
mtime, ownership, symlinks, hardlink *creation* on the destination, directory
structure, free-space reporting — works normally.

| Limitation | Effect |
|---|---|
| No inode numbers or link counts | Hardlinks in a **remote source** cannot be detected, so linked files are copied individually. Said once under `--verbose`. A local source copied to a remote destination keeps its hardlinks. |
| No `st_dev` | `--one-file-system` cannot see a mount point inside a remote source. |
| No xattr operations | Extended attributes, and the Linux ACLs that ride on them, are not copied when either end is remote. Mode, owner, and times still are. |
| Whole-second mtimes (SFTP v3) | Preserved times are truncated to the second. The unchanged-file comparison already tolerates one second, so re-runs still skip correctly. |
| No server-side copy or reflink | The kernel fast paths (`FICLONE`, `copy_file_range`, sparse hole-skipping) are local-to-local only; a remote copy streams bytes. Sparse files are written dense on the far side. |
| No remote hashing | `--checksum` reads the remote file's bytes to hash them. Correct, but it moves as much data as copying would; size+mtime remains the default for good reason. |

`--fsync`, atomic replace (`posix-rename@openssh.com`), hardlink creation
(`hardlink@openssh.com`), and the free-space guard (`statvfs@openssh.com`) use
OpenSSH extensions. A server without one degrades: fsync is skipped, replace
falls back to unlink-then-rename, the space guard switches off. No copy fails
because an optional extension is missing.

## How it is tested

`internal/vfs` and the engine's remote tests run against an **in-process SFTP
server** (`pkg/sftp`'s server implementation) wired to the client through pipes,
via the `vfs.StartTransport` seam. Real packets, real handles, real attribute
round trips, real extension negotiation — only the ssh subprocess is absent, so
the tests need no network, no credentials, and no `ssh` binary while still
exercising the code that runs against a real host. The ssh command line itself
is covered separately as a pure function (`sshArgs`).
