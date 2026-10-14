# Exclusive native TUI takeover

AEON-1288 implements the P2c owner-side takeover library and additive CLI command
node. I2 must wire these ports into the existing supervisor, C0 Host and CLI
command tree. This groundwork does not enable a live provider or change the
serving structured coordinator. No migration, dependency, permission or public
contract change is introduced.

After integration and separate provider qualification:

```text
aeon session take OWNED_ID --project KEY
aeon session take OWNED_ID --project KEY --hand-back
```

The first command explicitly claims local control. If that conversation already
has an owned PTY with no controller, it reconnects to that runtime. Esc and normal
keys belong to the official vendor TUI. `Ctrl+]` then `d` detaches; `Ctrl+]` then
`r` requests hand-back. EOF, signals, an unavailable server, a lost lease or a
disconnected stream restore the local terminal and detach. They never implicitly
start a structured writer. The second command explicitly requests hand-back and
waits for an authorized projection of the fresh structured successor. Accepted
and queued receipts alone do not report completed takeover, delivery or return.

The existing attachment upload and session-start attribution commands retain
their meaning. The new command node is deliberately unmounted in this slice.

`nativepty.Manager` holds one transition gate and uses C0's `WriterCoordinator`,
`PTYLauncher` and `Launcher`. Required integration obligations are:

- `Authority.WithWrite` authenticates the local kernel peer and current person,
  client, control capability, lease, exact binding and target permissions. It
  holds the supervisor's shared native/access-change fence through the final
  native write. All structured input, approval and control paths share this
  fence. Quiesce/close use that held fence rather than recursively acquiring it.
  Server permission changes still use `RequireTx` in their final database
  transaction; no database transaction is held over native I/O.
- `Qualification.Require` independently qualifies each mode's exact binary,
  account, tool/settings policy, native history and OS permission fence. Nil
  qualification keeps take unavailable. If an SDK callback is the only tool or
  approval fence, the TUI remains unavailable. An affirmative fake test never
  qualifies a live vendor.
- `Quiesce` fences new structured input, settles or expires callbacks, and
  checkpoints eligible receipts/finals before closing. Queued person input stays
  in P3a's already-authorized durable storage. Only an original input intent
  explicitly allowing the same conversation's successor is eligible for fresh
  permission checks and rebinding; this library never retargets chat/inbox rows.
- `CloseStructured` and `ConfirmExit` prove the owned root and group have exited.
  `PrepareSuccessor` reserves fresh current admission/grant, harness generation,
  runtime epoch, owned writer identity and private worker/tool proof. The profile,
  workspace, native conversation, model and permission ceilings stay bound.
  `CommitRuntime` explicitly commits the prepared binding, without silently
  changing it. Preparing/committing must retain occupancy on every possible fork
  and failed registration, including a nonnil runtime returned with an error.
- The runtime `Lifetime` belongs to agentd, separate from the bounded operation
  context. The supervisor retains and monitors every committed runtime handle.
  Manager is created once for an already-owned structured runtime; recreating a
  manager or reading a saved PID is never recovery/adoption authority.

Take quiesces and closes the structured runtime, verifies full exit, then opens
one agentd-owned PTY. Return quiesces control, closes and waits for the PTY,
verifies full exit, rechecks admission/grant and opens a fresh structured runtime.
Unknown exit, stale proof, failed launch, revoked permission or failed binding
commit leaves `recovering` with occupancy held and no control. Automatic retry or
an alternate writer is prohibited. Fresh controllers must explicitly attach to
the new runtime; controller/approval/native callbacks never transfer.

`ExecLauncher` uses the existing `golang.org/x/sys/unix` dependency on Darwin and
Linux. It opens a controlling terminal, starts a new process session, verifies
the process group and retains `ownedprocess.Lifetime` through exit observation
and group cleanup. It never signals after PID reaping. Its trusted provider
`CommandBuilder` supplies only the qualified official resume command, explicit
account environment and session-only settings overlay. No shell wrapper,
ambient daemon environment, pooled profile, credential copy, global settings
edit or vendor binary modification is introduced. Unsupported platforms refuse.

Only the current local controller may send bytes or resize. Browser and agent
callers cannot write or resize, even if they know public identifiers or another
tab's epoch. Sizes are 40×12 through 320×100, applied with `TIOCSWINSZ`; the default
is 120×36. Unchanged sizes are suppressed. Explicit reconnect or regained control
reapplies the size, and a failed resize does not publish new dimensions. Client
loss and no-holder periods keep the last size. PTY input is bounded to 8 KiB and
sequence-fenced; uncertain writes are never replayed, including a reconnect with
the same lease epoch. Local window storms are debounced for 150 ms.

Frames are bounded, transient local owner data with backpressure, not transcript
storage or browser publication authority. I2/P2a must separately qualify owner
watch consent, screen export and per-frame/keepalive access before advertising
`terminal_view`. Login/authentication PTYs, secrets and unsafe screens must never
be exported. Agents receive Events only. The local client has no device reply or
remote input callback. Export filtering cannot be inferred from PTY ownership.

The backend A2 tests cover storm debounce, only-on-change, bounds, two holder
tabs/viewers, immediate holder size on take, last size on hand-back, phone keyboard
shrink/restore, reconnect and regained lease. Browser measurement after
`fonts.ready`, hidden/Events segments, pinch zoom, visual viewport and pane layout
is P2e's responsibility; those conditions are not new fields in C0's Resize
schema. The backend always requires the current local lease and rejects sizing
while input is paused. This slice has no browser/UI changes or visual gate.

Behavior tests use injected clocks and explicit exit barriers, plus a compiled
test-child PTY witness. No subscription, model CLI, real account or live cutover
is used. Remote Go packages are `./internal/nativepty/...` and
`./internal/cli/...`; required static checks run only through the guarded LEAD
runner. Release, provider/export qualification, cross-family review and live
verification remain coordinator-owned.
