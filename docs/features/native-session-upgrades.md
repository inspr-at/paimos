# Drain-aware native session upgrades

AEON-1307 (AEON-1259 P3c) supplies a bounded upgrade lifecycle in
`internal/nativedrain` and `prepareNativeUpgrade` in `cmd/aeon-agentd`.
This is integration groundwork: I2 owns the serving-loop, supervisor and
dispatch wiring. No new command, route, automatic restart or live cutover is
enabled by this slice.

The coordinator owns the old live LEAD pilot freeze. The default local
configuration refuses upgrade preparation with `pilot_frozen` and leaves
ordinary approved operation intact. Only the coordinator may construct the
upgrade coordinator with `CoordinatorApproved` after lifting that freeze. This
is immutable local configuration; no request body or session label supplies it.

## Admission and ownership

I2 must use one drain coordinator for the entire host. Every native start,
resume, dispatch, takeover and hand-back that can create a writer must enclose
its complete admission, possible-fork journal, native open and registration in
`WithAdmission`. Register any uncertain fork in the existing owned registry and
shared ledger before leaving that section. The barrier does not replace current
server authorization, `RequireTx`, account/host reservations or process proofs.

`Begin` closes admission before any inventory read. Already admitted operations
remain counted until their registration finishes. Their presence visibly defers
the upgrade with `admission_pending`; inventory and close do not run while they
could publish a previously unseen writer. All later admission receives
`ErrDraining`. Failed attempts keep admission closed. Reopening it is not an
upgrade retry operation.

The owning supervisor implements the local `Runtime` port:

- `List(ctx, 128)` bounds its ledger read before allocation and includes live,
  possible-fork and uncertain writers. Truncation, duplicates and mismatched
  bindings defer the upgrade; registry disappearance is never exit evidence.
- `QuiesceAtBoundary` atomically checks foreground and background work under
  the owned writer lock and fences new inputs/tasks. Active, background or
  unknown work returns `ErrBusy` without closing it. The fence must remain in
  effect through close/checkpoint and retries. There is no stop-on-upgrade for
  active work, forced termination or saved-PID adoption.
- `Close` uses the owned runtime handle and graceful policy. Both observed root
  exit and complete process-group absence must match its exact process digest,
  runtime epoch and binding. `ConfirmExit` revalidates current ownership and
  evidence through the existing ledger under its ownership fence. A completed
  turn, idle state, API acknowledgement or root exit alone never suffices.
- `Checkpoint` durably records the exact native conversation/profile and
  recovery metadata after exit confirmation. A failed checkpoint survives
  removal from the live registry and is retried without closing the exited
  writer again. It contains no raw transcript, PTY bytes or credentials.
- `Flush` gives receipts and eligible explicit finals a bounded attempt even
  when active work defers the upgrade. It retains pending data under existing
  outbox limits and publication/retention grants. Success requires acknowledgement
  or a durable explicit rejection/expiry, never an in-memory queue acceptance.
  API failure reports `flush_unconfirmed` and cannot permit restart.

All runtime operations must use their current authority under the existing
writer/access locks. Only an owned, safe-boundary graceful close is allowed;
this package supplies no force authority or permission expansion. Every resumed
writer still needs fresh admission, runtime/harness proofs and approved history
mapping through P3b; preparation never starts a successor.

## Bounded attempt and owner status

An attempt has a maximum 20-second total budget, using the frozen C0 local
operation bound and an injected clock. `Status` is content-free local owner
status with the admission fence, pending admissions, per-session retention,
exit/checkpoint progress and flush result. The command helper prints that status
before preparation and again on success or failure. I2 must project it through
the existing authorized owner status surface; this slice does not amend C0.

An expired/cancelled attempt stays deferred. A callback that ignores cancellation
keeps the attempt occupied until it returns, preventing a second close/flush
attempt; its late completion cannot claim readiness. Adapters must honor the
context so repeated attempts do not leave permanent pending work.

`ready` requires every inventoried or retained writer to have confirmed exit and
a durable checkpoint, plus a confirmed delivery flush. This permits the
coordinator to consider a service upgrade; the helper itself never installs a
release or restarts a process. On any error, keep the serving loop, owner socket,
receipt/final path and occupancy holds alive, show the deferred reason, and retry
deliberately after its cause is resolved. Do not lift the pilot freeze through a
retry, remove uncertain holds, run `launchctl kickstart -k`, or add a persistent
session keeper.

## Verification

The named risk is an upgrade while LEAD dispatches losing or duplicating effects.
The behavior test holds an admitted launch at a registration barrier, rejects
later launches, retains a real fixture background writer, rejects incomplete
process-group exit, then proves exit → checkpoint → flush before readiness.
Checkpoint and API failures retain their progress without repeating a close.
The injected-clock test expires an uncooperative close, refuses an overlapping
retry and rejects late readiness. Inventory/provenance fixtures assert the
specific refusal, and the command test shows pilot/flush deferral honestly.

The admission test's negative control disables only the drain admission fence
on a private scratch branch. The guarded remote test must fail specifically with
`new launch during drain`. Restore the implementation branch and remove the
scratch branch afterward; keep no negative-control commit on the work branch.

Run committed HEAD through the guarded remote lane:

```sh
/Users/markus/.aeon/lead/aeon/remote-test.sh "$PWD" ./internal/nativedrain/... ./cmd/aeon-agentd/...
/Users/markus/.aeon/lead/aeon/ci-static-locked.sh
```

No browser or real vendor-account/model call is required. Source ownership is
already covered by `internal/native*/**` (S2) and `cmd/**` (S1). The slice adds only
its named Go test rows to the canonical tier manifest under the R0 grant; no API,
migration, table, permission or shared-fence caller is introduced. PR CI is
authoritative if a guarded remote lane refuses or reports incomplete validation.
