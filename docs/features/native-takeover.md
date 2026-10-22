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
  client, control capability, lease, exact binding and target permissions. Its
  `Access.Revision` is the current server session revision; take/return compare
  the captured action revision there, including changes caused by renewals.
  Public revision/generation projections come from the coordinator, not Manager.
  It holds the supervisor's shared native/access-change fence through the final
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

The PTY reader drains output independently while input waits for buffer space.
Writes remain serialized, and every resumed write rechecks closure, binding and
lease expiry. Frame dimensions use a separate short lock; resize and owned exit
observation can proceed while a paste waits.

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

Acceptance coverage is registered as ESSENTIAL in the same change:

- Quiesce and full exit before either successor: `TestTakeoverWaitsForBothOwnedExits`.
- Separate native TUI fence, with the serving writer retained on refusal:
  `TestTakeoverRequiresIndependentTUIFence`.
- Fresh runtime/harness/process proof and fixed conversation/profile:
  `TestTakeoverRejectsSuccessorIdentityOrProofReuse` and
  `TestTakeoverLaunchIdentityMustMatchConversationAndFreshProof`.
- Local holder input/size, stale control rejection and no uncertain replay:
  `TestPTYResizeTwoHolderTabsAndViewers`,
  `TestPTYInputRejectsStaleControlAndUncertainRetry` and the resize scenarios.
  Durable input policy remains in P3a's required coordinator quiesce/rebind port.
- Exit/admission/rebind failure retains occupancy:
  `TestTakeoverUnknownExitRetainsOccupancy`,
  `TestTakeoverRechecksAuthorityAfterExit`,
  `TestTakeoverFailedRebindKeepsPossibleFork` and
  `TestTakeoverUsesCurrentAuthorityRevision`.
- Client loss retains one writer without implicit return:
  `TestTakeoverClientLossAndExpiredLeaseDoNotRestart` and
  `TestNativeTakeClientRebindsAndDetachNeverReturns`.
- A full PTY input buffer does not block draining the child's output:
  `TestPTYUnixPasteDrainsOutputWhileInputIsBlocked` verifies an acknowledged
  8 KiB paste, exact child input/output and confirmed owned exit. A raw-mode
  barrier fills input before the child emits 128 KiB and then reads stdin.

FIX2 validation (2026-10-14): the blocked-paste regression reproduced the
deadlock on reviewed head `4a08173f` before the fix. Remote `nativepty` and `cli`
package tests and `nativepty -race` passed on implementation commit `f11a1d4ff`.
Guarded `ci-static --merge-main` exited 0 with 42 checks passed and no optional
skips. Canonical tier validation and affected selection also exited 0; selection
requires the FULL CI lane. Non-blocking review findings remain follow-up work.

## Route 3 owning host (AEON-1361 P1)

The C0 owning host now connects a qualified terminal runtime to the existing
account ledger, person-bound start, command journal and transient relay. A
terminal start uses the mode and exact conversation identity returned by the
authorized server start/profile policy. Local requests have no mode selector.
Only enrolled Claude Code and Codex CLI profiles are eligible; ordinary
structured starts continue without allocating a PTY. The released server
policy still selects structured starts. Production terminal launch, TUI
qualification, screen export and the person PTY command adapter remain closed.
The construction-only `store.Options.StartMode` port pins the approved provider
and profile matrix inside the existing person start transaction; nil keeps its
structured default. Its decision is rechecked on claim replay and initial
readiness reporting. No production composition installs a terminal matrix.

An initial command plan must bind the server conversation without inventing a
vendor resume reference. Its bounded readiness parser observes the native
conversation identity from provider output. A fork alone cannot produce the
host's applied start/take receipt. Startup output is held in a bounded RAM
spool; readiness failure, cancellation and excessive initialization output
retain the returned owned handle. The operation context never becomes the
runtime lifetime. Binary qualification and the host's current grant check run
again immediately before fork through `PTYLaunchSpec.Revalidate`.

`NativePTYAuthority` is the explicit integration port for package G. It must
prepare a payload-bound server operation for the independently authenticated
local person and resolve the viewer to its actual controller client. Its
`WithWrite` holds the shared access-change fence through the final native I/O;
no database transaction spans I/O. The host also holds the same per-owner lock
used by structured commands, stop, take and hand-back. The metadata journal
records the possible-write boundary before I/O and rechecks expiry and daemon
identity after that disk barrier. Holder and retired-controller checks precede
the possible-write marker; an input refused before this point is rejected and
does not fault the runtime or consume its input sequence. The existing
nativepty controller enforces exact binding, current actor/client/controller
epoch, monotonic sequence,
40×12–320×100 dimensions and changed-size-only resize. Reconnect reapplies the
holder's dimensions. The existing 150 ms client debounce is unchanged. An
uncertain write consumes its sequence and requires explicit recovery; it never
automatically injects input again. A socket bearer or paired agent supplies no
person authority. The released person bridge does not install this new port.

Terminal output is drained even when no export relay is installed. A qualified
relay uses its existing bounded RAM ring, per-viewer delivery authorization and
explicit eviction gaps. Missing source frames produce an explicit gap.
Transient authorization/export outages keep the owned terminal alive and keep
draining and counting its output. Only an omitted-frame count is retained during
the outage; recovery publishes a gap before fresh output. Definitive refusals
still cancel the owner lifetime and retain occupancy until confirmed settlement.
EOF, owner lifetime cancellation and deadlines use the same ownedprocess cleanup
owner as the launcher. Natural terminal exit is reported on the original start
operation through the existing private receipt route. The server rechecks the
paired lease, exact binding and root/group exit inside its access-change
transaction before releasing the root. Local occupancy is freed only after
that write succeeds. Unknown exit or a refused report keeps occupancy; Poll
reconciles an observed exit without another signal or writer. Supervisor Close
continues to refuse while a process may still exist and never signals it.

LEAD decision D4 supersedes raw transcript persistence for r140. Replay remains
RAM-only and is erased on retirement. The existing event log retains lifecycle
events, with observed output byte/frame counts and a cumulative SHA-256 content
hash in the existing event metadata column for the exact terminal epoch,
including an exited predecessor on hand-back. Public `NativeAuditEvent`
snapshots keep their strict existing schema.
PTY command journals contain receipt metadata and payload hashes. Once every
report of a settled PTY input or resize is acknowledged by the server, only
the latest 32 such local receipts and their native observations are retained.
Unreported or uncertain effects stay durable; typing reserves the final journal
slot for owner Stop even during a reporting outage. The server remains the
authoritative source for retired operation receipts. Raw terminal
bytes, reasoning, interim chat and tool bodies never enter these paths. Counts
describe observed output, not a durable or complete transcript. Existing final
delivery and its explicit retention authorization remain separate.

Qualification evidence in this package uses fake providers and compiled harmless
test children. Live acceptance (AEON-268) must still qualify the exact official
Claude Code/Codex command, profile, readiness parser, settings/tool ceiling,
native-history policy and OS fence. Initial server/profile terminal selection,
the package G person command adapter and independently qualified watch export
must be installed before enabling their production capabilities. This package
changes no operator accounts, host enrollment, browser frontend or deployment.

### P1 validation and handoff boundaries

The required remote affected set passed on implementation commit `7197331a3`:
`internal/nativepty`, `internal/nativeattach`, `internal/agentd`,
`internal/nativesessions/...`, `internal/reportercontract` and the additional
`internal/nativecontract` checks. Focused `-race` tests passed for nativepty,
host trust/transition/real-PTY fixtures and SQL lifecycle fixtures on
`9488ff871`; the final metadata SQL fixtures passed `-race` on `7197331a3`.
Guarded `ci-static --merge-main` exited 0 on `7197331a3`: 44 checks passed,
zero optional skips. The pinned test-child host witness covers both eligible
provider identities, including
bounded read output, holder input, a measured 132×40 resize and confirmed owned
exit. Exit barriers cover structured/TUI/structured switching; denied person,
viewer, controller, sequence, grant, profile and daemon cases reach no I/O.
Slow viewers remain within the existing 64-frame/256-KiB ring and receive an
explicit gap. Readiness failure retains its handle and exact owner stop path;
an unconfirmed group retains occupancy. Fifteen new tests are ESSENTIAL.

The SQL fixture proves that the existing event log stores the final predecessor
counts/hash on hand-back and terminal exit metadata on natural EOF. Neither
path stores raw output. There is no raw-transcript retention grant in r140.

Minimal additions outside the primary host/launcher files are:

- `internal/nativecontract/ports.go`: local conversation, pre-fork validation
  and independently observed readiness ports; no public DTO change.
- `internal/nativesessions/daemon/proof.go`: bounded content-free terminal
  lifecycle evidence on the existing private proof.
- `internal/nativesessions/store/types.go`: construction-only initial mode
  policy, with the released structured default.
- `internal/nativesessions/store/claim.go`: current start/profile mode selection
  inside the existing admission transaction.
- `internal/nativesessions/store/commands.go`: dispatch observed natural exit
  through the existing private receipt route.
- `internal/nativesessions/store/production.go`: current ownership checks and
  lifecycle settlement in the existing access-change transaction.
- `internal/nativesessions/store/start.go`: the narrow policy validator and
  content-free metadata beside existing lifecycle snapshots.
- `internal/nativesessions/store/control_test.go`: independent readiness and
  SQL lifecycle/persistence qualification fixtures.
- `internal/nativesessions/store/production_test.go`: the natural exit report
  barrier in the existing paired-server fixture.
- `scripts/ci/go-test-tiers.json`: only the fifteen new ESSENTIAL test rows,
  written through the canonical tier writer.

The new `internal/agentd/native_pty_unix_test.go` remains covered by the existing
S2 `internal/agentd/**` audit slice. No migration, DSAR column inventory, public
route, response schema, version, account enrollment or capability enablement is
changed. The coordinator retains cross-family review, merge and release gates.

### P1 second fix round

The three blocking host regressions were reproduced against reviewed head
`7b13d50a4` using a detached test-only snapshot `a7b0ab3d1` on the approved remote
runner. Typing hit `buffer_full` at resize 257; first input and expired-holder
input were incorrectly marked uncertain; HTTP 503/408/429 stopped the output
reader before its second publish. The test-only commit is outside the delivery
branch. Regression tests also cover continued owner control after a refused
input, output loss gaps on recovery, cancellation on definitive HTTP 403 and
retention of unreported/uncertain metadata across journal reopen.

Outside the write set: `internal/nativereceipts/journal.go`,
`internal/nativereceipts/receipt.go` and
`internal/nativereceipts/receipts_test.go` add the narrow acknowledged-PTY
retirement and reserved Stop slot needed to fix the shared-journal exhaustion.
`scripts/ci/go-test-tiers.json` adds only the four new ESSENTIAL rows through
the canonical writer. Production qualification and package G/P2 enablement
boundaries remain as described above.

The optional broad `agentd -race` run found six unrelated fixture races; the
same six were reproduced by a separate remote run on unchanged reviewed head
`7b13d50a4`. The first five race with `watchHarness` reading the test clock in
`harness_notifications.go:107`; the last races on the fake queue call count:

- `TestPauseHeartbeatUsesExistingInboxAndNeverStopsProcess`
- `TestScheduledPauseWaitsAndEscalationIsDeliveredOnce`
- `TestPauseWakeHintAnchorsBeforeHeartbeatRoundTrip`
- `TestUncertainDeliveryDoesNotMaskHeartbeatFailure`
- `TestInboxUnconfirmedDoesNotReinject`
- `TestSupervisorQueueHintsUnavailableBackoff/401`

Their code is unchanged by this slice. No assertion was skipped or weakened;
the required normal affected suite and focused PTY race checks are separate
from this additional broad race probe. The export regression asserts the
existing relay protocol: a gap closes the subscription, then a fresh authorized
snapshot and the exact next terminal frame prove recovery.

FIX2 validation on code head `c09af095e`: the full required remote affected
set passed with `-count=1`: `nativepty`, `nativeattach`, `agentd`,
`nativesessions/...`, `reportercontract`, plus `nativereceipts`,
`nativeintegration/...` and `nativecontract`. Focused remote `-race -count=1`
passed every `TestNativeHost.*PTY` case and the receipt-retirement safety test.
The earlier broader race run also passed every package except the six baseline
fixture failures named above. Guarded `ci-static --merge-main` passed all 44
checks with zero optional skips. No web source, unit or browser spec changed.

The existing PR rehearsal failure was checked read-only: the amd64 build failed
downloading Go modules from `proxy.golang.org` with network timeouts. Its dependent
release rehearsal consequently failed. The non-required policy preview reported
`no trusted gate/verdict`; the coordinator must post the fresh review verdict.
Neither path was rerun or bypassed by this worker. No origin push, deployment,
model CLI or product review gate was run.

### P1 third fix round

The CI no-browser safety suite exposed an unverified-root cleanup race: the
root's exit event can precede disappearance of its whole process group. Root
identity failure now sends SIGKILL once and waits for group disappearance with
at most 200 probes and 199 ten-millisecond waits. ESRCH confirms completion;
EPERM remains evidence of an observable group awaiting reaping. No retry signals
an unverified or possibly reused process group. If cleanup cannot be confirmed,
the supervisor retains its lock and ownership journal, including when that
journal is empty. No invalid start identity is written to make cleanup appear
successful.

Two deterministic regressions hold group disappearance behind a barrier and
prove that uncertain cleanup exhausts its bounded budget without releasing the
supervisor lock. Both fail on reviewed head `dce094fbc`, using test-only snapshot
`a43f422c4` outside the delivery branch: cleanup settled before the barrier, and
the supervisor lacked the unconfirmed-exit result. The focused negative run used
the permitted single small local fallback after the remote runner refused at
its global two-run capacity limit. It ran two
Node tests, no browser, with two expected failures and zero skips. The temporary
branch was removed after returning to the assigned delivery branch.

Outside the write set: `scripts/playwright-processes.mjs`,
`scripts/playwright-safe.mjs` and `scripts/playwright-safe.test.mjs` contain only
the cleanup fix, its lock-retention hook and regressions required by this CI
brief. The existing `test:browser-safety` command owns these script tests;
no new source file or test-tier registry entry is needed. No web application
source, UI spec, production capability or account policy changes.

The original hosted `web-shard (16)` failed before executing tests: its
`Install Chromium system dependencies` step exhausted twelve minutes while apt
downloads stalled. The aggregate web job consequently failed, and tier
measurements reported missing `web-shard-16` evidence and incomplete browser
case reports. These hosted jobs need fresh CI evidence after the coordinator
pushes. The non-required policy preview still requires the coordinator's fresh
trusted verdict. This worker does not run, replace or bypass that gate.

FIX3 validation: the required full affected Go set passed remotely with
`-count=1` on implementation commit `2285a0716`: `nativepty`, `nativeattach`,
`agentd`, `nativesessions/...`, `reportercontract`, plus `nativereceipts`,
`nativeintegration/...` and `nativecontract`. Guarded remote
`ci-static --merge-main` passed on `2629bcf3c`: all 44 checks, zero optional
skips, including the full no-browser safety suite and both positive regressions.
The existing live-root ESRCH assertion is unchanged. No UI spec is affected;
no browser was launched by the worker. No origin push, deployment, model CLI
or review gate was run.

### P1 fourth fix round

Hosted shell installation could spend 150 seconds on each update and install,
then repeat both three times inside a four-minute workflow step. Shell installs
now try the runner's cached indexes first and refresh after failure. All retries
share a 220-second budget, including each root timeout's kill grace, leaving
20 seconds before the unchanged workflow deadline. Deterministic clock fixtures
cover the healthy cached-index path and exhausted root cleanup budget. Both
regressions are exercised through the existing
`TestRootAptTimeoutReapsBeforeRetry` entry in the Go tier inventory.
Both fail remotely on reviewed head `23437389f` with tests only, in scratch
snapshot `ff4d59543`: the cache check observes an unnecessary update, and
the budget check observes three independent 150-second root deadlines.

The ticket-panel shortcut spec previously sent `s` when the title appeared,
before the project permission response necessarily enabled status changes.
It now holds that response behind an explicit barrier, proves the initial
disabled state, releases the response, and waits for the enabled status control
before sending the shortcut. The original status, priority, assignee and write
precondition assertions remain in place. This is test synchronization only;
the product's permission checks and keyboard policy still apply.

Outside the write set: `scripts/ci/bounded-apt.sh`,
`scripts/check-apt-bounds.test.mjs` and `scripts/testdata/apt-fixture.mjs`
contain the shell installation fix and regressions required by this CI brief;
`web/tests/ticket-panel.spec.ts` adds the permission barrier and readiness
assertions for its reported CI failure. Existing source files and registered
test entries own these checks; no new source file or tier entry is needed.

FIX4 validation on implementation head `56c64ac20`: all required native Go
packages passed remotely on Linux with `-count=1`: `nativepty`, `nativeattach`,
`agentd`, `nativesessions/...`, `reportercontract`, plus `nativereceipts`,
`nativeintegration/...` and `nativecontract`. The focused
`TestRootAptTimeoutReapsBeforeRetry` passed remotely, including all six Node
cases and both new regressions. The entire `ticket-panel.spec.ts` passed
46/46 through the approved remote browser entry point with one worker;
the supervisor reported zero remaining owned processes.

The additional full `scripts/releaseworkflow` Linux run encountered existing
fixtures requiring `/bin/bash`, absent on the NixOS runner (for example,
`TestBuilderVersionAssertionFailsClosed`: `/bin/bash: bad interpreter: No such
file or directory`, and `TestReleaseBinariesRefuseWarmOrUnverifiedGo`:
`fork/exec /bin/bash: no such file or directory`). Those fixture files are
unchanged. After the remote static entry point refused for capacity, the full
package passed in the permitted single-package local fallback with
`GOMAXPROCS=2 go test -p 2 -parallel 2 ./scripts/releaseworkflow -count=1`.
No browser or full static suite ran locally.

The full guarded `ci-static --merge-main` remained unproven after bounded
capacity retries: the remote entry point returned exit 3 for its two-run cap
or host guard. The complete static set, including its web unit infrastructure,
tier/shard and audit checks, therefore needs the coordinator's CI handover.
This round retains 95% status until that evidence exists. No origin push,
deployment, review gate or model CLI was run.


### P1 merge-main round

The normal merge incorporates main `83a13846c` without changing the reviewed
PTY implementation. The four conflicts are confined to apt installation
fixtures and the browser supervisor test imports. Cached-index-first shell
installation is retained alongside main's temporary mirror fallback, offline
bundles, security configuration, fetch-only retry rules and root cleanup.
Main's 210-second shared budget tightens the earlier 220-second limit and
includes the final kill grace. Deterministic assertions exhaust both allowances
and verify the exact install/refresh order; unsafe bundle tests still prove
that rejected inputs never reach offline dpkg. Both browser supervisor imports
and their ownership regressions remain present.

All 74 main-only files match the merged main byte-for-byte, every branch-only
file matches reviewed head `9ce8ffb9f`, and the merged Go tier inventory retains
every test row from both parents. No migration is introduced or renumbered.
The full required native package set, including receipts, integration and
reporter contract checks, passed remotely with `-count=1` on `d03d36bce`.
Focused local apt checks passed 4/4, root verification and uncertain group
ownership checks passed 4/4 without a browser, and migration policy tests
passed 43/43. The complete 17-case apt regression then passed remotely through
`TestRootAptTimeoutReapsBeforeRetry` on `d03d36bce`. The existing interrupted
fixture/lifecycle regression also passed remotely on that head. Full static
validation on `d03d36bce` passed 43 checks with zero optional skips, including
apt bounds, migrations, OpenAPI, audit/tier/shard checks, lint/typecheck and the
full no-browser supervisor safety suite. Its `releaseworkflow-tests` check
exceeded the existing 120-second harness limit. The focused apt and interrupted
fixture regressions passed separately; the full package timeout remains
unproven and is not classified as a known environment exception.

The final guarded static retry was refused at the two-run capacity cap.
Fresh `ticket-panel.spec.ts` validation was also refused by host-level and
capacity guards. No browser or full static suite ran locally. This merge round
therefore remains at 95% pending coordinator CI evidence for the full static
release-workflow check and the browser spec. No test was skipped or removed,
no timeout was extended, and no product behavior outside conflict reconciliation
was changed. No origin push, deployment, review gate or model CLI was run.
