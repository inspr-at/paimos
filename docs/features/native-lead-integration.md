# Native LEAD integration: Round 4 handoff (AEON-1290)

I1 remains **incomplete**. Round 4 separates the exact paired daemon's runtime
authority from the ORIGINAL actor's current project control and native grant.
`RuntimePermissions`, pairing issuance, roles and key ceilings are unchanged.
The server now mounts an explicit default-off pilot constructor; the owning
paired supervisor still has no enabled native-session callbacks. No live LEAD
handover, vendor call, daemon restart, push or deployment was performed.

## Implemented

The account and shared-quota counters now add native roots only where
`run_id IS NULL AND released_at IS NULL`. The existing AEON-1251 worker
predicates, including conservative `exit_unconfirmed` holds, remain intact.
Queued, starting, running, idle, taken and uncertain roots retain their slots
without depending on a heartbeat, daemon generation or viewer attachment.
Selection, readiness, routing and other callers of those counters inherit the
same accounting. Pairing host capacity, engine admission and routine admission
also include all unreleased roots on their computer.

Native admission uses separate worker-run and owned-session exclusions. A claim
retry projects out exactly its own root; siblings remain counted once and
worker sidecars contribute no extra slot. Account archive checks native holds
before `DisconnectAccount` could revoke enrollment or final-report authority.

`nativesessions.New(Services)` now requires qualified Store, FrameSource and
FinalSink services and dispatches the authored C0 routes. It preserves the
disabled constructor, native error/contract headers, authentication and person
boundaries, bounds bodies before decoding, rejects mismatched route/body tenant
or target identifiers, and distinguishes accepted work from launched/delivered
work. Public egress re-applies the person projection, including native-context
and mutation responses. Qualified P2a relay/viewer authorities can supply the
existing stream and live-page HTTP handlers, with their frame and keepalive
fences. No public route, schema, permission or contract version changed.

The HTTP module stays independent of concrete account/inbox/start storage to
avoid an auth → native module → start store → accounts → inbox test import
cycle. Its `RouteProject(ctx)` is a selector for the integrating Store adapter,
which must pass it through `store.WithProject`; it grants no authority.
Private daemon routes require a `DaemonRequests` implementation to resolve
current paired identity and private proof. Missing qualified extensions refuse
the operation; body generation identifiers never substitute for pairing proof.

Pairing drain now retains enrollments for every unreleased null-run native root.
Computer-level finalization also checks roots directly, including roots whose
enrollment is absent. Immediate computer, enrollment and self-disconnect,
computer removal, account disconnect and request-expiry revocation refuse with
`native_roots_unreleased` before changing ownership. Neither a display state,
stale heartbeat, old daemon generation nor a viewer's departure is release
evidence. Released roots and worker sidecars do not block final drain.
The changes use the existing tenant/tree/pairing fence and add no lock caller.

## Round 4 authority and exact root context

`store.Claim` retains the exact pairing `c.key_id`, live enrollment and
`harness.worker` checks. Its final transaction re-reads the immutable original
actor, calls `requireStart` against that actor's CURRENT `harness.control` and
`run.create`, and revalidates the original native grant. The daemon no longer
needs `harness.control`; no substituted principal or broadened key is used.

`RequireNativeSessionTx` now requires an explicit `NativeSessionAuthority`.
It checks the live exact paired key and worker permission, re-reads the
persisted ORIGINAL actor/intent/policy, checks that actor's current project
control and canonical ownership, and requires current native/tool/settings/
history consent in the SAME transaction. Missing implementations refuse with
`qualification_missing`; native grant refusals keep their exact cause. Stored
policy is a pin, never current consent. A delegated original key must be in the
sorted key-use batch before the transaction begins.

`nativeintegration.RootAuthority` implements the local native-admission port.
It resolves exactly the held null-run root, current harness/runtime epoch,
project, daemon, account enrollment digest, workspace, policy and launch pins.
It excludes only that root from current account/host admission, and calls the
new two-party final validator under tenant/tree/pairing/matrix fences. A stale
binding, released root, changed account or changed launch config cannot validate.
It does not manufacture a worker run. Profile changes remain unavailable unless
a separate explicit authority is supplied.

`nativeintegration.New(Options{})` is the server's explicit disabled pilot
composition. Disabled construction discards supplied runtime ports. Enabling
requires qualified C0 Store/Frames/Finals plus Host/Launcher and current native
admission authority. This is a reviewable seam, not finished production
composition: no qualified authority, local launcher, command adapter, final
publisher or attach client is installed by the server/paired daemon yet.
Automatic routine `LeadRuntime` remains independently unqualified.

The native SDK bridge now retains the consumed input correlation on its own
explicit `chat_final`. The existing fake SDK assertion requires both the exact
answer and its input ID. This closes the dropped final correlation seam; it
neither installs a durable publisher nor qualifies background-task/compaction
metadata. Native refusals also retain the closed C0 reason and the store's
precise HTTP status, including revocation and private read 404s.

## Remaining ownership and integration blockers

The Round 4 authority grant is used; `types.go`/`provision.go` need no grant or
ceiling change. These are the remaining dependency source files identified by
tracing the production final, local attach and native CLI paths together:

| Boundary file (and its behavior tests) | Exact reason a grant is needed |
| --- | --- |
| `internal/chat/native_final.go` | `StoreNativeFinalTx` requires an agent sender equal to the harness's paired principal, then checks that SAME sender for `chat.receive` and `chat.send`. Neither scope is in the real paired runtime ceiling. Passing the original person fails its agent/sender identity checks; inventing broader scopes or a coordinator principal is forbidden. Native attribution and original-actor authorization must be separated under the final write fence while retaining current chat context, drain, retention and permission checks. |
| `internal/nativesessions/receipts/repository.go`, `internal/nativesessions/receipts/final.go` | `FinalAuthority.Sender` is documented as the immutable original actor and is passed directly as both authorization identity and attributed sender to the chat helper. The person-invoked root's original actor and its actual paired runtime are different parties. The final authority/result and final call must carry and validate them separately, in concert with the chat helper, without altering ordinary final routes or manufacturing an agent actor. |
| `internal/nativeattach/socket.go`, `internal/nativeattach/client.go` | Receipt GETs send only `limit`/`after`, while the C0 local adapter requires exact runtime epoch, harness ID, binding revision and generation on every GET. The socket helper receives no generation; its caller must supply the already-pinned session generation. Filling omitted selectors from a server guess would weaken the record binding. |
| `internal/nativeattach/stream.go` | The stream GET carries the three binding coordinates but omits the required `generation`. `ValidateReadQuery` therefore refuses before subscribing, even on a correctly attached viewer. The client must send its pinned generation; the local adapter's strict check stays intact. |
| `internal/cli/native_session.go`, `internal/cli/native_session_unix_test.go` | The existing coordinator start helper and its strict test emit the P1a `aeon-native-session/1` envelope with `NativeSessionStartRequest`, rather than C0's `NativeLocalStart`/`aeon-native/1` envelope and native operation result. The helper also has no persisted person-issued native start selector. Safely preserving native flags, exact resume/after-exit provenance and the ORIGINAL person requires updating the owning helper/test, rather than duplicating its private socket/authentication lifecycle in the permitted legacy attribution files. |

These files were inspected read-only and were not modified. A1 production
composition and A5's combined fake-native smoke still need their I1 adapters in
the granted integration/agentd files after these dependency boundaries are
closed. The start store's mandatory authority/observer and the receipt
repository's mandatory current receipt/exit/final-drain/retention authorities
remain unimplemented in production. No permissive callback, legacy chat route
workaround, fake worker run, fabricated person/agent, or new credential was added.
The C0 starter also deliberately refuses resume/after-exit: the owning
supervisor's existing pinned-exit and one-writer mechanism must be integrated
before a self-handover can be claimed; removing those fields is not proof.

## Acceptance handoff

| Item | Result |
| --- | --- |
| A1: C0 Host/Launcher, start Store, relay, receipts/final sink and attach wiring | PARTIAL. Qualified HTTP dispatch and P2a stream mount seam from Round 2, plus an explicit default-off composition constructor. Production ports are not installed; final-authority and attach/CLI dependency files above remain outside the grant. |
| A2: person-invoked native path separate from automatic LeadRuntime | PARTIAL. Actual paired-key claim code keeps the original person, current project control and native grant separate. The server's pilot composition is separate and default off. No enabled person-invoked supervisor path is installed. |
| A3: native roots in legacy account/host admission; exact root context | IMPLEMENTED AT THE PORT, not live-qualified. Prior occupancy/drain holds remain; the real-pairing native claim/helper and exact-root local-admission adapter are implemented. Fresh database behavior verification is incomplete due to remote capacity refusals. |
| A4: default account, confined adapter and legacy CLI compatibility; inventories | PARTIAL. Account default, confined branch, attribution and legacy attachments preserved. Two new own ESSENTIAL rows and one own shared-fence caller row registered; canonical manifests pass. Fresh compatibility suites still await remote/PR CI. |
| A5: fake start/input/exact turn/explicit final/interrupt/confirmed exit; detach survives | NOT COMPLETE. The SDK final correlation seam is fixed with a strengthened existing regression; the authority regression uses actual pairing and barriers. Full combined I1 smoke and its negative control remain unimplemented because production final/attach wiring is blocked. Fresh per-guard negative-control runs are also incomplete. |
| A6: concrete disabled pilot for coordinator self-handover | PARTIAL. The server explicitly mounts the default-off pilot and its refusal behavior passes locally. Complete qualified ports and the existing pinned after-exit writer transition remain to be composed before coordinator self-handover. No enablement or live-soak claim. |

Z1's validated person-only `terminal_viewer_url` remains in the C0 projection
and is stripped on agent egress. Publishing the pilot URL and connecting the
live LEAD path to the existing terminal link still require full I1/I2 wiring.
`G_TERMS`, `G_LEAD_GRANT` and `G_P1_LIVE` remain coordinator/person gates;
no 48-hour live-soak or enablement evidence is claimed.

## Prior Round 3 verification

Round 3 PASS ownership behavior at `26ed89b9b`: guarded remote
`./internal/agentpairing/... -run TestNativeRootOwnershipSurvivesPairingDrainAndRemoval
-count=1 -v` exits 0. The regression covers queued, starting, running, idle,
taken and uncertain roots; exact refusal cause; immediate whole/enrollment/self
disconnect; removal; direct account disconnect; sibling enrollment isolation;
and retention of enrollment/account/key through repeated drain until persisted
release. Released roots and worker sidecars do not retain the daemon afterward.

Round 3 PASS affected Go at `671b74c80`: guarded remote `-count=1` exits 0 for
`./internal/agentpairing/...`, `./internal/agentaccounts/...`,
`./internal/agentd/...`, `./internal/harness/...`,
`./internal/reportercontract/` and `./internal/nativesessions/...`, including
start, store, interactions, receipts and stream. Both new regressions pass.
`TestNativePilotCannotExpandPairedRuntimeAuthority` uses the actual role/key
issued by person-approved pairing. It establishes successful `harness.worker`
authority before requiring exact `authz.ErrForbidden` for `harness.control`,
`harness.force_stop` and `chat.receive`; those scopes are absent from the stored
key too. No fixture expands that daemon's scopes or substitutes a member role.

The same run verifies the earlier constructor boundary, alias/shared-quota/host
counters, own-root claim replay and native account identity/context checks.
These dependency tests prove their individual seams; they do not qualify full
production I1 composition. The previously failing Round 2 account fixture is
corrected, and this complete account-suite rerun is green. No existing test was
loosened or skipped.

Round 3 PASS pairing negative control: the guarded runner tested detached stash
snapshot `2b48ce986` with the two pre-fix lifecycle/removal implementations and
only the new exported error symbol retained so the current regression compiles.
It exits 1 at the intended assertion: immediate disconnect returns HTTP 200
instead of 409, with computer and enrollments revoked while the native root is
still held. The same work branch and positive implementation were restored.
No negative-control commit was left on that branch. This is ownership-regression
evidence, not the still-missing combined I1 smoke's negative control.

Round 3 static at `25a0c4e3d` exits 1: 40 checks pass, zero optional checks
are skipped, and `web-tiers` plus `web-classification` exceed their existing
120-second bounds. No timeout was raised and no check was skipped.

Round 3 PASS static retry at `8ebe62e42`: guarded `ci-static-locked.sh
--merge-main` runs the underlying remote `node scripts/ci-static.mjs
--merge-main` and exits 0, with all 42 checks passing and zero optional skips.
This includes strict Go/web tiers, source ownership, shared inventories, vet,
migrations, reporter/OpenAPI, web typecheck and lint. Earlier exit 3 attempts
were remote-capacity refusals, not green results. No local fallback or unguarded
static check was used. The final documentation-only commit records this result;
no production/test implementation changed after these passes.

Round 3 PASS: `node scripts/test-tiers/cli.mjs manifests --check` and
`node scripts/test-tiers/affected.mjs --base origin/main` exit 0. The affected
planner conservatively selects FULL (`R1 full: manifest promotions unavailable:
scripts/ci/go-test-tiers.json`). That is selection evidence, not an executed
whole-repository suite. `git diff --check` exits 0. PR CI remains authoritative.

NOT RUN: full I1 combined fake-native smoke and its negative control, because the
full runtime composition is incomplete. No browser check is applicable. No
origin push, deployment, review gate, model CLI call, daemon restart, vendor call
or live pilot was performed. Approved remote-runner temporary refs are not
origin pushes. At the Round 3 handoff progress remained 55% with the native claim/root-authority
dependency files named BLOCKED; that authority boundary is addressed in Round 4.
A1–A6 still are not complete.

R0 additions are exactly these ESSENTIAL rows in `scripts/ci/go-test-tiers.json`:

- `internal/agentaccounts/TestNativeRootsFenceWorkerAliasAndProjectOwnClaim`
- `internal/agentaccounts/TestNativeRootFencesLegacyHostAdmission`
- `internal/nativesessions/TestQualifiedNativeRoutesBindCallerAndTargetBeforeDispatch`

Round 3 adds exactly these further ESSENTIAL rows through the same canonical
writer:

- `internal/agentpairing/TestNativeRootOwnershipSurvivesPairingDrainAndRemoval`
- `internal/agentpairing/TestNativePilotCannotExpandPairedRuntimeAuthority`

No source ownership, shared-fence, route, DSAR, permission, migration, web-tier
or shard rows were added because this round introduces none of those entries.
Shared counter/module/lifecycle/tier edits are limited to the explicit grant
and own tests. Round 3 changes only lifecycle/removal, their existing test file,
the own tier rows and this handoff.

## Round 4 verification and R0 handoff

PASS at `6bb3bf5b7`: guarded remote `./internal/agentaccounts -count=1`
exits 0 in the affected two-package run, including the updated existing
`TestNativeSessionFinalAdmissionRechecksIdentityContextAndCapacity`. That same
run exits 1 overall because the new real-pairing fixture was missing its
matching account-owner stamp. The corrected fixture's next targeted run at
`f1c5fae0c` fails on a UUID/text schedule parameter; that parameter is corrected
in `4f3ef7144`. The regression now also invokes the real separate person
`capacity/approve` route (`bc532436b`), since connect-only pairing itself does
not grant ongoing account work. Its control-route check uses the issued bearer
and rejects the exact missing control scope, rather than accepting any 403.
These earlier failures are not reported as green. Later exact-root and
canonical-owner additions still require a fresh database run.

PASS local at `af3c615f2`: after multiple guarded remote capacity refusals, the
ONE permitted small-package fallback runs `GOMAXPROCS=2 GOTOOLCHAIN=local go
test -p 2 ./internal/nativeintegration -run
TestPilotDefaultsOffAndRejectsIncompleteQualification -count=1` and exits 0
(`ok .../internal/nativeintegration 0.352s`). It proves all public routes remain
`feature_disabled` with qualified-looking ports and incomplete enablement refuses
`qualification_missing`. It performs no model/browser/database work and is not
substituted for the missing database, SDK, compatibility or integration checks.

INCOMPLETE: subsequent guarded remote retries for native integration, native
session/store, accounts, pairing, agentd, harness, reporter, CLI and both binary
packages are refused with exit 3 (global remote slots full or CI pool 4/4).
Both guarded `ci-static-locked.sh --merge-main` attempts also exit 3 before any
checks run. No guard was bypassed, no full local suite/static check was run,
and no timeout/exclusion was loosened. Fresh SDK correlation, per-guard negative
controls and combined I1 smoke evidence are not claimed. PR CI is authoritative.

PASS: canonical `node scripts/test-tiers/cli.mjs manifests --check`,
`node scripts/test-tiers/affected.mjs --base origin/main` and `git diff --check`
exit 0. Affected selection is FULL (`R1 full: manifest promotions unavailable:
scripts/ci/go-test-tiers.json`); selection is not execution.

Round 4 R0 additions are exactly two ESSENTIAL Go rows, written through the
canonical tier writer:

- `internal/nativesessions/store/TestPairedNativeStartAndRevalidationKeepSeparateAuthorities`
- `internal/nativeintegration/TestPilotDefaultsOffAndRejectsIncompleteQualification`

The sole new shared-fence row is `nativeintegration/pilot.go` under
`agentpairing.Lock`, in source entry order: tenant/tree/pairing then matrix and
record rows, before any event. Existing native source ownership covers both new
native integration files and the new claim regression; the coordinator-owned
slice file is untouched. No route/schema/version, migration, DSAR, permission,
role/key ceiling, web-tier or shard row changes were made. Shared edits are
limited to the granted claim/account/module/server paths and own R0 rows.

Progress remains below 100%. One BLOCKED note names all remaining boundary
files above and the incomplete remote checks. No origin push, deployment,
review gate, other model CLI, live daemon restart or LEAD switch was performed.

Final Round 4 handoff: 65%, below completion. The last full affected remote
retry after `bc532436b` and the last static retry after `300ab83d5` were both
capacity-refused (exit 3). The work branch is clean; the final documentation
commit records this incomplete result. No negative-control or throwaway commit
was created or left on the work branch.
