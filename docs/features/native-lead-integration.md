# Native LEAD integration: Round 2 handoff (AEON-1290)

I1 remains **incomplete and blocked**. Round 2 implements native root occupancy
in the assigned legacy admission paths and the qualified C0 HTTP constructor.
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

## Remaining ownership and integration blockers

The Round 2 grant covers `agentaccounts/groups.go`, `native_root.go` and
`nativesessions/module.go` plus their tests. It does not cover these additional
pairing lifecycle files:

| File | Blocker before live pilot |
| --- | --- |
| `internal/agentpairing/lifecycle.go` | `finalizeDrain` revokes an enrollment when no active or uncertain worker run exists. It does not check unreleased native roots, so a live/idle native root can lose the paired key needed for reconciliation or final delivery. |
| `internal/agentpairing/remove.go` | Computer removal's unsettled-work check and `DisconnectAccount` consider worker runs only. They can archive/revoke a native root's account/computer. The in-scope account archive guard protects that entry point, not every pairing entry point. |

These paths need an explicit ownership grant or a coordinator-owned prerequisite
fix and behavior tests. They were inspected read-only and not changed. The
original brief requires stopping at an additional write-set boundary; the
Round 2 grant does not transfer these files.

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
| A1: C0 Host/Launcher, start Store, relay, receipts/final sink and attach wiring | PARTIAL. Qualified HTTP dispatch and P2a stream mount seam implemented. Production Store/Host/Launcher composition and combined fake-native smoke remain outstanding. |
| A2: person-invoked native path separate from automatic LeadRuntime | INCOMPLETE. Existing automatic and native runtime callbacks remain disabled; no person-invoked production composition installed. |
| A3: native roots in legacy account/host admission; exact root context | PARTIAL. Listed account/shared-quota/pairing/engine/routine admission counts and own-root replay exclusion implemented. Pairing lifecycle safety is blocked above; exact root runtime context wiring remains outstanding. |
| A4: default account, confined adapter and legacy CLI compatibility; inventories | PARTIAL. No account-home, adapter or CLI behavior changed. Three own behavior tests classified ESSENTIAL via the canonical R0 writer; no new route, permission, table or source ownership row. |
| A5: fake start/input/exact turn/explicit final/interrupt/confirmed exit; detach survives | NOT IMPLEMENTED. No combined I1 smoke exists; individual dependency tests do not prove this integration. |
| A6: concrete disabled pilot for coordinator self-handover | INCOMPLETE. Default-off production behavior preserved; pilot bootstrap and live self-handover are not claimed. |

Z1's validated person-only `terminal_viewer_url` remains in the C0 projection
and is stripped on agent egress. Publishing the pilot URL and connecting the
live LEAD path to the existing terminal link still require full I1/I2 wiring.
`G_TERMS`, `G_LEAD_GRANT` and `G_P1_LIVE` remain coordinator/person gates;
no 48-hour live-soak or enablement evidence is claimed.

## Verification

PASS: guarded `ci-static-locked.sh` (underlying remote
`node scripts/ci-static.mjs --merge-main`) exits 0: 42 checks passed, zero optional
checks skipped, including strict `go-tiers`, test-tier manifests, source ownership,
shared inventories, migration and reporter/OpenAPI checks. Earlier host-guard
refusals (exit 3) and the old feature document's public-name failure are not
counted as green; the updated document passed the rerun.

PASS A1/A4 constructor boundary:
`TestQualifiedNativeRoutesBindCallerAndTargetBeforeDispatch` and
`TestDisabledNativeRoutesAuthenticateWithoutStarting` passed in the remote
`./internal/nativesessions/...` suite. Accepted start stays HTTP 202; malformed,
oversized, unauthenticated and cross-target writes do not reach mutation storage.
This proves the HTTP seam, not full Host/Launcher/receipt composition.

PASS A3 counters and ownership:
`TestNativeRootsFenceWorkerAliasAndProjectOwnClaim`,
`TestNativeRootFencesLegacyHostAdmission`,
`TestNativeRootHoldsCountUntilVerifiedRelease` and
`TestNativeSessionFinalAdmissionRechecksIdentityContextAndCapacity` passed on
the remote committed HEAD with `./internal/agentaccounts -run TestNative
-count=1 -v` (exit 0). The alias test verifies the exact capacity wait and legacy
public route refusal, native archive protection, own-root exclusion, sibling
occupancy, worker sidecar exclusion and reopening only after release.

PASS A3 claim/replay dependency:
`TestCoordinatorClaimsReserveQuotaBudgets` and
`TestStartRegistrationIdempotencyAndPersonViewer` passed in the remote
`./internal/nativesessions/store` suite, including exact own-hold claim replay.

PASS compatibility/dependency suites: remote `./internal/agentd/...`,
`./internal/harness/...`, `./internal/agentpairing/...`,
`./internal/reportercontract/` and `./internal/nativesessions/...` passed.
The broad account run initially failed the new fixture (quota identity, public
refusal expectation, then retained alias fields); these were corrected, and the
named native account rerun above is green. No pre-existing test was loosened or
skipped. The broad all-account command was not repeated after that final fixture
correction; authoritative PR CI remains required.

PASS: `node scripts/test-tiers/affected.mjs --base origin/main` exits 0 and
conservatively selects FULL because the Go manifest changed. This is selection
evidence, not an executed full suite. `git diff --check` exits 0.

NOT RUN: full I1 combined fake-native smoke and negative control, because the
full runtime composition is incomplete. No browser check is applicable. No
push to origin, deployment, review gate, real model call, daemon restart or live
pilot was performed. The approved remote runner's temporary test refs are not
origin pushes. Progress remains below 100 with the pairing files named BLOCKED.

R0 additions are exactly these ESSENTIAL rows in `scripts/ci/go-test-tiers.json`:

- `internal/agentaccounts/TestNativeRootsFenceWorkerAliasAndProjectOwnClaim`
- `internal/agentaccounts/TestNativeRootFencesLegacyHostAdmission`
- `internal/nativesessions/TestQualifiedNativeRoutesBindCallerAndTargetBeforeDispatch`

No source ownership, shared-fence, route, DSAR, permission, migration, web-tier
or shard rows were added because this round introduces none of those entries.
Shared counter/module/tier edits are limited to the explicit grant and own tests.
