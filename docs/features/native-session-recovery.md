# Deliberate native session recovery

AEON-1305 / AEON-1259 P3b provides `internal/nativerecovery` against the
frozen C0 contract. This is integration groundwork; I2 owns the supervisor,
server and route wiring. `hidden_from_release_notes: true`. No live resume,
provider enablement, new route, migration, permission or dependency is introduced.

`Reconcile` distinguishes four local observations. A disconnected viewer may
reattach to the same binding; it cannot change the writer. An API outage retains
the current writer, shared capacity and P3a delivery data. Daemon loss and child
exit expire all old controllers and approval callbacks, then require qualified
absence evidence before settling occupancy. A lost heartbeat, saved PID/native
ID, missing RAM entry, idle turn, completed answer or final-persistence receipt
is never exit proof. Recovery itself never signals or adopts a process.

The owning supervisor implements `Runtime` under its existing mutation lock.
`Read` uses its original possible-fork journal and shared ledger. `ProveAbsent`
observes exact binding/attempt provenance. A known child requires its exact
process digest, observed root exit and complete process-group absence after its
start time. A possible fork without a recorded identity requires independently
qualified attempt-scoped absence; a PID lookup cannot supply that proof.
`ConfirmAbsent` independently verifies the attestation and commits the existing
journal/ledger settlement under that fence. A never-attempted reservation may
be abandoned only after its original journal proves no possible fork. Failure
retains occupancy and exposes `ownership_lost` or `recovery_needed`.

`Resume` defaults to `person_continue` by the actual owner. It requires current
revision, fresh runtime epoch and harness/worker proof, current server policy,
account/host/model admission and a freshly qualified local history mapping on
the workspace owner's own vendor account. A raw saved native session ID does
not meet this requirement. The conversation, account binding, workspace, profile
revision and immutable tool/settings/permission scope must agree. Final-drain
claims never authorize launch. The successor receives no reconstructed initial
input, approval or controller. Takeover/hand-back remains P2c's separate lifecycle.

`Prepare`/`Revalidate` must reauthorize inside the final server transaction with
`RequireTx` under the access-change fence; local preflight does not replace this
check. The claim deadline is monotonic, derived from the server's TTL minus
request elapsed time. Revalidation and deadline/effect checks repeat after the
durable decision barrier. `Attempt` performs the final fork-boundary check and
writes possible-fork evidence in the **existing** owner journal/shared ledger
before opening the unmodified pinned vendor runtime. It must register and retain
every uncertain fork before returning. I2 encloses the complete operation in
`nativedrain.WithAdmission`. Runtime callbacks honor the 20-second context bound
and retain the writer fence until the callback has ended.

Bounded continuation is permitted only by an existing `bounded_preapproved`
grant following daemon loss. Aggregate usage must be known and retain the
original start time across generations. The original expiry, runtime, turn and
token ceilings constrain the remaining successor budget; retry cannot renew
them. The private recovery decision journal consumes the single restart before
possible native open, including a crash between those two boundaries. A failed
or ambiguous attempt is not automatically retried. A later explicit owner action
still needs fresh absence/history/admission proof. Uncertain external effects
block automatic continuation.

Recovery reads P3a's bounded receipt pages to expose unresolved possible writes.
Manual continuation requires current reconciliation authority tied to the digest
of that exact effect set. The unresolved receipts remain visible after resume;
context continuity does not establish exactly-once dispatch, merge or deployment.
Recovery never reinjects input or replays approvals. P3a's retained explicit finals
continue through their independent original final-only publication/retention
grant and stable ID, even after daemon or child loss. Pending finals are reported
as pending, and final persistence never releases a process slot. The status's
pending-final count covers the dedicated local P3a store; it is owner-only and
must not be projected as another session's public data.

The dedicated private `agentsetup.Store` uses owned physical paths and its
existing atomic write/fsync behavior. `recovery-<owned-session>.json` retains
content-free request/actor digests, predecessor/successor bindings, at-most-once
decision state and restart consumption. It contains no PID, transcript, prompt,
PTY bytes, approval payload or vendor credentials. This is a small decision
snapshot, not a second process ledger or append-only event log. Limits are 128
conversation snapshots, 32 decisions per conversation and 64 KiB per file,
checked before decoding or adding records. There is no eviction of uncertain
decisions. Ambiguous disk writes close further admission on that handle. The
local metadata is personal, located by owner state root + tenant/session;
it creates no database table requiring DSAR inventory changes.

Acceptance evidence:

1. `TestLossKindsRetainWriterUntilExactAbsence` distinguishes all four loss
   kinds and rejects wrong attempt/runtime/digest, root-only exit and future proof.
2. `TestCrashForkWindowsNeverDuplicateOrReleaseUncertainWriter` uses the actual
   admission journal/shared ledger and barriers before possible fork, after its
   journal and after fake native open; coordinator restart never duplicates a
   spawn or releases uncertain occupancy.
3. `TestResumeRequiresCurrentProofPolicyHistoryAndExpiresCallbacks` covers fresh
   worker/runtime proof, immutable account/history/policy, drain-only refusal,
   revocation/deadline changes at the decision barrier and idempotent completion.
4. `TestBoundedContinuationUsesOriginalWindowAtMostOnce` uses an injected clock
   and aggregate budget to reject renewed/exhausted/unknown windows and a second
   continuation after coordinator restart.
5. `TestInputAckCrashKeepsEffectsVisibleAndNeverReinjects` and
   `TestFinalPersistenceCrashRetainsOutboxAndUncertainSlot` establish barriers
   before/after native acknowledgement and final persistence against P3a's real
   private store. Native input is written once, old callbacks expire, final-ID
   replay persists once, and unresolved process occupancy remains held.

`TestRecoveryJournalBoundsAndLocalAuthorityRefusal` additionally rejects corrupt
and overfull decision arrays before adoption and refuses JSON export of local
history, snapshot, absence and successor authority. These seven named tests are
the exact ESSENTIAL rows added to `scripts/ci/go-test-tiers.json` under the R0
grant; no existing row was changed or removed.

All tests use fake native transport and injected clocks, never real vendor/model
calls. The source belongs to the existing S2 `internal/native*/**` audit glob.
Only this slice's Go test rows are added to R0's canonical ESSENTIAL manifest;
no shared-fence, table, permission, route or web-shard rows are needed.

Required checks on committed HEAD:

```sh
/Users/markus/.aeon/lead/aeon/remote-test.sh "$PWD" ./internal/nativerecovery/...
nix develop --offline -c node scripts/test-tiers/cli.mjs check go --strict
nix develop --offline -c node scripts/test-tiers/cli.mjs check web --strict
/Users/markus/.aeon/lead/aeon/ci-static-locked.sh
```

Browser validation is not applicable. A guarded-lane refusal (exit 3) is an
explicit incomplete handoff to authoritative PR CI, never a passing check.
Only the coordinator runs cross-family review, pushes, merges, deploys or enables
live recovery. No rollout or account/profile change is authorized by this slice.

Builder verification on the completed source:

- **PASS 1–5:** all seven tests in the permitted single small local package
  (`GOMAXPROCS=2`, `go test -p 2 -count=1 ./internal/nativerecovery`) exit 0.
- **PASS:** offline strict Go inventory (5,153 native cases) and offline strict
  web inventory exit 0, through the machine-wide guarded local slot. Web deps
  were absent and installed once from the unchanged lockfile; no browser ran.
- **PASS negative control:** a temporary scratch branch removed only restart
  consumption enforcement. The bounded-continuation test failed specifically
  because a forbidden second continuation returned success instead of
  `grant_missing`. Source was restored and the scratch branch removed; no
  throwaway commit remains.
- **INCOMPLETE:** the prescribed guarded remote package run and
  `ci-static-locked.sh` both refused with exit 3 for occupied remote runner slots.
  Neither is reported green or retried in a loop. Authoritative PR CI must
  complete the remote/static validation before merge. I2 production wiring and
  coordinator review/release decisions remain outside this slice.
