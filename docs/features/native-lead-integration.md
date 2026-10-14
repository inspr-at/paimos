# Native LEAD integration: Round 3 handoff (AEON-1290)

I1 remains **incomplete and blocked**. Round 3 closes the granted pairing
drain/removal/disconnect ownership gap, alongside Round 2's native root occupancy
and qualified C0 HTTP constructor. The next dependency is the incompatible
paired-runtime authority described below; its writers are outside this grant.
Production still mounts `NewDisabled`; paired agentd has no native-session
callbacks configured. No live LEAD handover, vendor call or daemon restart was
performed. This is not an enabled pilot or live-soak result.

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

## Remaining ownership and integration blockers

The Round 3 grant covers the earlier pairing lifecycle blocker. The next
production boundary appears before native-root context or C0 composition can
be qualified:

| File | Blocker before live pilot |
| --- | --- |
| `internal/agentpairing/types.go`, `internal/agentpairing/provision.go` | Actual person-approved pairing issues both the role and runtime key from `RuntimePermissions`. It includes `harness.worker`, but omits `harness.control` and `chat.receive`. The production paired supervisor uses this runtime key. These issuance files are outside the grant. |
| `internal/nativesessions/store/claim.go` | Final native start claim requires the authenticated daemon to pass `harness.control`, in addition to the original actor's start authority. The real paired role/key cannot pass it. `currentDaemon` also pins the exact pairing `c.key_id`, so a separately scoped key is not interchangeable. The existing start-store fixtures supply broader synthetic daemon scopes and a member role. This dependency file is outside the grant. |
| `internal/agentaccounts/native_session.go` | `RequireNativeSessionTx` repeats `harness.worker` and `harness.control` against that same paired daemon before account/context/capacity checks. Exact-root runtime revalidation cannot pass with the actual issued authority. This dependency file is outside the grant. |

The coordinator must qualify a narrow native execution delegation or grant the
dependency files for a fix that validates the original actor's current native
grant separately from the ordinary runtime ceiling. Adding control/chat scopes
to every ordinary runtime, passing a fabricated principal, substituting the
CLI coordinator identity or omitting these final checks would violate the
authority boundary. No such workaround was installed. These files were inspected
read-only and not changed. The
original brief requires stopping at an additional write-set boundary; the
Round 3 grant does not transfer these files.

Full I1 composition is also still outstanding. The start store requires
transactional native actor/tool/settings/history authority and a trusted local
ledger/proof observer. The receipt repository requires current receipt/exit
evidence and original final-drain/retention authority. Those packages currently
provide interfaces and test implementations. I1 must supply production adapters,
map the owning supervisor to C0 Host/Launcher, compose durable commands,
receipts/finals and attach, and re-use exact root context. Neither existing
project lead intent nor automatic routine qualification may be treated as a
native grant. No permissive authority callback or fake worker run was added.

## Acceptance handoff

| Item | Result |
| --- | --- |
| A1: C0 Host/Launcher, start Store, relay, receipts/final sink and attach wiring | PARTIAL. Qualified HTTP dispatch and P2a stream mount seam implemented in Round 2. Production composition cannot pass native claim with the actual paired runtime authority; the dependency files above are not granted. |
| A2: person-invoked native path separate from automatic LeadRuntime | INCOMPLETE. Existing automatic and native runtime callbacks remain disabled; no person-invoked production composition installed. |
| A3: native roots in legacy account/host admission; exact root context | PARTIAL. Legacy counts and own-root replay exclusion implemented in Round 2. Round 3 pairing drain/removal/disconnect retention is implemented and remotely proven. Exact root runtime revalidation remains blocked by the real paired daemon's missing control authority. |
| A4: default account, confined adapter and legacy CLI compatibility; inventories | PARTIAL. Account-home, adapter and CLI behavior remain unchanged. Round 3 adds a real-pairing ceiling regression and two own ESSENTIAL rows via the canonical R0 writer. It does not install an enabled native compatibility path. |
| A5: fake start/input/exact turn/explicit final/interrupt/confirmed exit; detach survives | NOT IMPLEMENTED. No combined I1 smoke exists; individual dependency tests do not prove this integration. |
| A6: concrete disabled pilot for coordinator self-handover | INCOMPLETE. Default-off production behavior preserved; pilot bootstrap and live self-handover are not claimed. |

Z1's validated person-only `terminal_viewer_url` remains in the C0 projection
and is stripped on agent egress. Publishing the pilot URL and connecting the
live LEAD path to the existing terminal link still require full I1/I2 wiring.
`G_TERMS`, `G_LEAD_GRANT` and `G_P1_LIVE` remain coordinator/person gates;
no 48-hour live-soak or enablement evidence is claimed.

## Verification

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
origin pushes. Progress remains 55% with the native claim/root-authority
dependency files named BLOCKED; A1–A6 are not complete.

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
