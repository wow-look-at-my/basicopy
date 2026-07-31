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

## Connections (automatic, like everything else)

**One connection per file being transferred at that moment**, dialed on demand
and capped at 16. That is not a performance tweak, it is what "parallel" means
over SFTP: an sftp-server answers one request at a time, so two transfers
sharing a connection are two transfers in a queue — the far side runs them one
after the other however many workers the copy has.

It is invisible on ordinary files, where each request is answered as fast as the
disk allows. It is the entire story when a read has to *wait*:

- **Cloud-backed placeholders** (OneDrive/Dropbox files-on-demand) that must be
  downloaded on first read.
- Cold storage, a disk spinning up, a network filesystem behind the remote.

On one connection those wait one at a time, and a copy of such a tree runs at
exactly one file's worth of latency after another — the thing a hand-written
loop of parallel `scp` commands avoids, only because each `scp` brings its own
server process.

How the pool grows:

- A transfer takes a connection that is carrying none. Failing that, it takes
  one that is carrying a transfer but has nothing outstanding right now — the
  far side is keeping up, and queuing behind it costs nothing worth a handshake.
- Otherwise the pool dials another connection and the transfer waits for it (or
  for an earlier transfer to finish). Waiting is the point: a file handle cannot
  move to another connection once it is opened, so piling onto a connection that
  is already waiting is a decision you cannot take back.
- At 16 connections, or after a dial fails, transfers share connections instead
  of waiting.
- Once there is more than one connection, the first is kept clear of transfers.
  Directory listings and stats go there, so the walk never ends up queued behind
  a file that takes a minute to read and stops feeding the workers.

**What stops it running away** is that it never decides how much parallelism to
use — it follows the transfers the copy already has in flight, and *that* number
is the worker controller's measured decision. A copy that gains nothing from
parallelism keeps a small `W` and therefore a small pool; a single-file copy uses
exactly one connection; a copy of two files against a fast server usually stays
on one, because the first connection is idle between requests whenever the second
transfer starts.

Two details make dialing safe rather than clever:

- **Extra connections run with `BatchMode=yes` and `ConnectTimeout=10`.** A host
  that needs an interactive credential can never be asked for it a second time
  mid-copy, and a host that became unreachable fails the dial promptly instead of
  leaving transfers waiting on a handshake that will never complete. Either way
  the pool freezes at its current size and the copy continues. Neither option is
  imposed on the first connection, which is the user's own ssh session.
- **Dials happen one at a time**, in the background, so a run never opens a burst
  of simultaneous handshakes (which `sshd`'s `MaxStartups` would start dropping).

### Why not "just pipeline harder" on one connection

Requests *are* pipelined — up to 64 in flight per file, which is what hides
round-trip latency and is why a single connection is fine for ordinary files.
But pipelining cannot fix a server that is *blocked*: the requests behind the
blocked one are queued, not overlapped. Nor can it fix a single TCP flow's
congestion window on a lossy long-haul link, or the fact that OpenSSH does cipher
and MAC work for a connection on one core.

Measured on this repo's test box (loopback SSH, 4 cores, 1.1 GiB in 60 files):
13.0 s confined to one connection, 3.8–5.1 s with the pool following the
transfers (6–7 connections), no flags on either run. And on files that are slow
to read, which is the case the pool exists for: `TestSlowRemoteReadsRunInParallel`
takes 5.6 s pinned to one connection — exactly the sum of its delayed reads —
and ~1.1 s with the pool.

### What the worker pool does when nothing is moving

The connection pool only helps if the copy keeps asking for concurrent
transfers, and the controller that decides `W` had the same blind spot: its rule
is "the last increase did not raise throughput, so `W` is high enough", and a
stalled copy has *zero* throughput at every `W`. Zero is never greater than
zero, so it would conclude one stream is enough and stay there.

So a stalled interval — every worker occupied, and not one byte, file, or
directory finished — is reported to the controller as such (`control.Sample`'s
`Blocked`), and it grows instead of concluding. A stall says nothing about `W`
being too high, and the only harm it can do is leave latency un-overlapped.
Once bytes flow again the ordinary plateau loop resumes and trims back whatever
the stall bought.

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
