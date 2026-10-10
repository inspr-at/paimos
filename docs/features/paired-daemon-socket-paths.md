# Paired daemon socket paths

Paired mode uses `<setup-root>/daemon/agentd.sock`. If that exceeds the
platform's socket path budget (100 bytes on macOS, 104 on Linux, allowing for
`sun_path` overhead), it uses `~/.aeon/run/<state-hash>.sock`, with 16 hex
characters identifying the daemon state directory. Both fallback directories
are owned by the user and mode 0700; symlinks and unsafe existing directories
are rejected. The selected socket is recorded privately in `daemon/control.json`.
Attach, setup/status and capacity clients read that reference; local control
can do the same with `control --setup-root PATH` (exclusive with `--socket`).
Existing generation-specific socket references remain readable while their
listener exists. No pairing data migration is required.

`HOME` is needed only for the fallback, so short and existing legacy socket
paths still resolve when it is unset. If the service and shell resolve different
fallbacks, the client reports its resolved home, setup root and socket alongside
the recorded socket; paths under the client's home are shortened to `~`.

`pair`, `setup` and paired `serve` reject an unrepresentable path before
creating pairing state or contacting the instance. Choose a shorter setup
root (`--state-root` for pair/setup, `--setup-root` for serve). Every local
listener takes an exclusive non-blocking lock on `<socket>.lock` before touching
the socket or token and retains it through shutdown cleanup. The lock file is
an owned mode-0600 regular file opened without following symlinks; it is retained
across restarts and is never deleted or renamed by startup, recovery or shutdown.
After acquiring the lock, startup compares the descriptor's device and inode
with the file named relative to the pinned private directory handle and retries
a bounded number of times if they differ.
Interrupted flock syscalls retry; only contention maps a flock error to busy.
The descriptor is close-on-exec, so harness children cannot keep the lock alive.
On macOS, a short directory flock serializes only the lock-file open: concurrent
`O_CREAT|O_NOFOLLOW` opens can otherwise return `ENOENT` during creation. It is
released before taking the lifetime file lock; open errors remain errors.
A second start reports "agentd is already running for this state
root" and leaves the active listener and token untouched, even when its accept
queue is full. Clients never take the lock.

The kernel releases the lock after a crash, including SIGKILL. The next owner
validates all stale socket artifacts through the private directory handle before
removing any: the socket, token (even without a socket), obsolete owner record,
and legacy `.s` plus eight hex digit quarantine names. Each must be owned by the
current uid, mode 0600 and single-linked, with the expected socket or regular-file
type. Symlinks, hardlinks, foreign owners and unsafe modes refuse startup.
The verified lifetime lock is the only cleanup authority. Recovery and shutdown
never probe the socket: on macOS even a live listener can refuse connections
when its accept queue is full. All cleanup checks and removals are relative to
the same pinned private directory handle. Lock identity is checked again before
each removal. Shutdown closes the listener and removes only its recorded
socket/token inodes while still holding that lock, then releases it. Losing lock
identity leaves residue for a verified owner to recover. Interruption likewise
leaves stale artifacts for the next owner; no quarantine is needed.
This advisory protocol serializes cooperating daemons. A same-uid process that
deletes or replaces the lock file or directory can disrupt a running daemon;
this is outside the protection boundary, since it can already signal or kill
the daemon. The no-symlink, owner, mode and link checks protect against accidental
or foreign-uid artifacts, not hostile same-uid path mutation.
Unrelated directory entries are preserved.
The private token and local control authorization rules remain unchanged.
