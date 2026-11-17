# Copying over SSH

`basicopy` treats a remote host like any other filesystem. Either end of a copy may be written the way `scp` and `rsync` write it:

```sh
basicopy ~/photos --target-dir myotherpc:/home/mhayine/backup
basicopy myotherpc:/home/mhayine/whatever --target-dir ./local
basicopy notes.txt --target-file me@myotherpc:/srv/notes.txt
```

Every feature works the same on both sides. This also covers the pre-write space guard, the stall watchdog, and crash-safe temp-file-plus-rename publication.

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

A colon *before* the first `/` makes an argument remote — the same rule `scp` uses, so muscle memory transfers. On Windows a single-letter prefix stays a drive letter (`C:\Users`), not a host.

Sources may name several different hosts in one run. Each gets its own connection. A copy with a remote source *and* a remote destination works and relays the bytes through this machine (SFTP has no server-to-server transfer).

## The transport: OpenSSH, not a reimplementation

basicopy runs `ssh -s <host> sftp` and speaks SFTP over the subprocess's stdio, the same approach rsync takes. That single decision is why there are no connection settings to learn:

- `~/.ssh/config` applies in full.
- The agent, hardware keys, `known_hosts`, and password/2FA prompts all behave exactly as they do for `ssh` itself. Every prompt is the real `ssh`'s own. basicopy shows it on the terminal and remembers the answer for the connections. Those connections follow (see [Prompts](#prompts-one-answer-for-every-connection)).
- Any host you can `ssh` into, you can `basicopy` to. Any host you cannot, the error you get is OpenSSH's own.

basicopy has no connection flags of its own. A second set only duplicates configuration the user already maintains for every other SSH tool. Each flag is a way to get a copy incorrect.

## ssh options that are not in your config: `-o`

Some settings exist only for one run. A deploy script opens a control socket at a path it chose. A CI job holds a key in a temporary file. `-o` (long form `--ssh-option`) hands such a setting to ssh, in `ssh_config` format, exactly as `ssh -o` and `scp -o` take it. It repeats.

```sh
# Log in once, then copy over that login without a second prompt.
ssh -M -S /run/user/1000/ctl.sock -fN deploy@host
basicopy -o ControlPath=/run/user/1000/ctl.sock ./site --target-dir deploy@host:/srv/site

# A key and a known_hosts file that live outside ~/.ssh.
basicopy -o IdentityFile=/keys/deploy -o UserKnownHostsFile=/keys/known_hosts ./site --target-dir host:/srv/site
```

basicopy does not read the options. It passes each to every ssh process it starts. That includes the connections that widen the pool. ssh keeps the first value it sees for a setting. The order on the command line is the precedence:

| Source | Wins over |
|---|---|
| `ConnectTimeout=10` on a pool-widening connection, and `BatchMode=yes` on it until basicopy has answered a prompt (see [Prompts](#prompts-one-answer-for-every-connection)) | everything below |
| the user and port written in the path (`user@host`, `ssh://host:port`) | `-o User`, `-o Port` |
| `-o` | `~/.ssh/config` |

A control socket carries every connection as a session on the login, whether it comes from `-o ControlPath` or from `ControlMaster` in your config. Nothing prompts twice. `sshd` limits the sessions on one login (`MaxSessions`, 10 unless the server says otherwise). Past that limit ssh reports `Session open refused by peer` and tries a login of its own. That login gets the answers you already gave in this run. If it needs one you did not give, the pool stops growing and the copy continues on the connections it has.

A control socket is not needed to avoid repeated prompts. It also puts every transfer on one TCP connection, which costs speed on a long or lossy link.

## Connections (automatic, like everything else)

**One connection per file being transferred at that moment**, dialed on demand and capped at 16. That is not a performance tweak. It is what "parallel" means over SFTP. An sftp-server answers one request at a time, so transfers sharing a connection are transfers in a queue. The far side runs them one after the other however many workers the copy has.

It is invisible on ordinary files, where each request is answered as fast as the disk allows. It is the entire story when a read has to *wait*:

- **Cloud-backed placeholders** (OneDrive/Dropbox files-on-demand) that must be downloaded on first read.
- Cold storage, a disk spinning up, a network filesystem behind the remote.

On one connection those wait one at a time. This is and a copy of such a tree runs at exactly one file's worth of latency after another — the thing. A hand-written loop of parallel `scp` commands avoids, only because each `scp` brings its own server process.

How the pool grows:

- A transfer takes a connection that carries none. Failing that, it takes one that carries a transfer but has nothing outstanding right now — the far side keeps up. Queuing behind it costs nothing worth a handshake.
- Otherwise the pool dials another connection and the transfer waits for it (or for an earlier transfer to finish). Waiting is the point. A file handle cannot move to another connection once it is opened. Piling onto a connection that is already waiting is a decision you cannot take back.
- At a set value connections, or after a dial fails, transfers share connections instead of waiting.
- Once there is more than one connection, the first is kept clear of transfers. Directory listings and stats go there. The walk never ends up queued behind a file that takes a minute to read and stops feeding the workers.

**What stops it running away** is that it never decides how much parallelism to use. It follows the transfers the copy already has in flight, and *that* number is the worker controller's measured decision. A copy that gains nothing from parallelism keeps a small `W` and therefore a small pool. A single-file copy uses exactly one connection. A copy of multiple files against a fast server usually stays on one. This is because the first connection is idle between requests when the second transfer starts.

Details make dialing safe rather than clever:

- **Extra connections never ask you a new question, and run with `ConnectTimeout=10`.** A connection that widens the pool gets only the answers you gave to the first one. A host that became unreachable fails the dial promptly instead of leaving transfers waiting on a handshake that will never complete. When a dial fails the pool freezes at its current size and the copy continues. Neither limit is imposed on the first connection, which is the user's own ssh session.
- **Dials happen one at a time**, in the background, so a run never opens a burst of simultaneous handshakes (which `sshd`'s `MaxStartups` will start dropping).
- **A dial that still waits when the copy ends is stopped.** No ssh process stays behind to wait for a touch or an approval.

## Prompts: one answer for every connection

Every connection in the pool is a separate ssh login on its own TCP connection. A key with a passphrase, a password, or a security-key PIN is asked for again on each. basicopy asks once.

```
$ basicopy ~/photos --target-dir nas:/backup
Enter passphrase for key '/home/me/.ssh/id_ed25519':
```

basicopy makes itself the askpass program of each ssh it starts (`SSH_ASKPASS`, with `SSH_ASKPASS_REQUIRE=force`). Every prompt comes back to the running copy. The copy shows it on your terminal, reads a secret without echo, and keeps the answer in memory until the run ends. A later connection that asks the same question gets the same answer. Nothing goes to disk. No agent and no control socket is involved, so each connection stays its own login.

- **A pool connection gets only answers you already gave.** Suppose it meets a question you never answered. Then such as a one-time code or a changed host key, basicopy stops that ssh before it replies to the host. It says so once and the copy continues on the connections it has.
- **A incorrect answer gets another try on the first connection.** ssh repeats a prompt when the answer failed, and basicopy asks you again. A pool connection does not try a failed answer twice.
- **A security key that needs a touch needs a touch for each connection.** That is the key's own rule: each login is a new signature. basicopy shows ssh's request (`Confirm user presence for key ...`) for each new connection. The pool grows as you touch. A touch you do not give ends as a failed login when the key gives up, and the pool keeps its size.
- **A key in an agent waits inside the agent.** basicopy cannot see that request. When a new connection waited a few seconds to log in, basicopy says so once.
- **Your own askpass program still works.** If your environment sends ssh prompts to a program (`SSH_ASKPASS`), basicopy asks through that program under the rules ssh applies, and still asks once.
- **With no terminal and no askpass program**, ssh does its own prompting as before and a pool connection runs with `BatchMode=yes`.
- **An ssh older than OpenSSH `8.4` ignores `SSH_ASKPASS_REQUIRE`** and prompts by itself. basicopy notices that no prompt reached it and keeps `BatchMode=yes` on pool connections. A key with a passphrase stays on one connection there.
- **On a Windows machine this is off**, and ssh prompts as before. It is not verified that the native `ssh.exe` can start basicopy as its askpass program.
- **Ctrl-C at a prompt** ends the prompt. The login fails with ssh's own error.

### Why not "just pipeline harder" on one connection

Requests *are* pipelined. Up to 64 in flight per file, which is what hides round-trip latency and is why a single connection is fine for ordinary files. But pipelining cannot fix a server that is *blocked*: the requests behind the blocked one are queued, not overlapped. Nor can it fix a single TCP flow's congestion window on a lossy long-haul link, or the fact. That OpenSSH does cipher and MAC work for a connection on one core.

Measured on this repo's test box (loopback SSH, cores, 1.1 GiB in multiple files): 13.0 s confined to one connection, 3.8–5.1 s with the pool following the transfers (6–connections), no flags on either run. And on files that are slow to read, which is the case the pool exists for: `TestSlowRemoteReadsRunInParallel` takes 5.6. S pinned to one connection — exactly the sum of its delayed reads — and ~1.1 s with the pool.

### What the worker pool does when nothing is moving

The connection pool only helps if the copy keeps asking for concurrent transfers, and the controller that decides `W` had the same blind spot. Its rule is "the last increase did not raise throughput, so `W` is high enough". A stalled copy has *zero* throughput at every `W`. Zero is never greater than zero. It will conclude one stream is enough and stay there.

So a stalled interval — every worker occupied, and not one byte, file, or directory finished. It is reported to the controller as such (`control.Sample`'s `Blocked`), and it grows instead of concluding. A stall says nothing about `W` being too high, and the only harm it can do is leave latency un-overlapped. Once bytes flow again the ordinary plateau loop resumes and trims back whatever the stall bought.

## What SFTP cannot carry

These are protocol limits, not omissions. Everything else — content, mode, mtime, ownership, symlinks, hardlink *creation* on the destination, directory structure, free-space reporting — works normally.

| Limitation | Effect |
|---|---|
| No inode numbers or link counts | Hardlinks in a **remote source** cannot be detected, so linked files are copied individually. Said once under `--verbose`. A local source copied to a remote destination keeps its hardlinks. |
| No `st_dev` | `--one-file-system` cannot see a mount point inside a remote source. |
| No attribute change that stops at a link | A symlink recreated on a **remote destination** gets the login's owner, not the source link's. Setting it would change the file the link points at. |
| No xattr operations | Extended attributes, and the Linux ACLs that ride on them, are not copied when either end is remote. Mode, owner, and times still are. |
| Whole-second mtimes (SFTP v3) | Preserved times are truncated to the second. The unchanged-file comparison already tolerates one second, so re-runs still skip correctly. |
| No server-side copy or reflink | The kernel fast paths (`FICLONE`, `copy_file_range`, sparse hole-skipping) are local-to-local only; a remote copy streams bytes. Sparse files are written dense on the far side. |
| No remote hashing | `--checksum` reads the remote file's bytes to hash them. Correct, but it moves as much data as copying would; size+mtime remains the default for good reason. |

`--fsync`, atomic replace (`posix-rename@openssh.com`), hardlink creation (`hardlink@openssh.com`), and the free-space guard (`statvfs@openssh.com`) use OpenSSH extensions. A server without one degrades: fsync is skipped, replace falls back to unlink-then-rename, the space guard switches off. No copy fails because an optional extension is missing.

## How it is tested

`internal/vfs` and the engine's remote tests run against an **in-process SFTP server** (`pkg/sftp`'s server implementation) wired to the client through pipes, via the `vfs.StartTransport` seam. Real packets, real handles, real attribute round trips, real extension negotiation — only the ssh subprocess is absent. The tests need no network, no credentials, and no `ssh` binary while still exercising the code that runs against a real host. The ssh command line itself is covered separately as a pure function (`sshArgs`). `dats/ssh.dats` checks it on the built binary, with a stand-in `ssh` on `PATH` that records its arguments.

Prompts are the exception, because the behavior under test is OpenSSH's. `internal/vfs/realssh_unix_test.go` starts a real `sshd` on the loopback interface and logs in with the real `ssh` and a key that has a passphrase. It checks that the person is asked once and that each pool connection is a new ssh login. A key that needs a touch is tested the same way. That key is with OpenSSH's own stand-in security key (`sk-dummy.so` from the `openssh-tests` package) and a wrapper around `ssh-sk-helper` that takes the time of a touch. CI installs these programs.

On a machine without them the tests skip. `internal/askpass` tests the terminal prompt on a pseudo terminal.
