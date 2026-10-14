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

Historical Round 2 PASS: guarded `ci-static-locked.sh` (underlying remote
`node scripts/ci-static.mjs --merge-main`) exits 0: 42 checks passed, zero optional
checks skipped, including strict `go-tiers`, test-tier manifests, source ownership,
shared inventories, migration and reporter/OpenAPI checks. Earlier host-guard
refusals (exit 3) and the old feature document's public-name failure are not
counted as green; the updated document passed the rerun. Round 3 results are
recorded separately below and do not inherit this green status.

Round 3 PASS ownership behavior at `26ed89b9b`: guarded remote
`./internal/agentpairing/... -run TestNativeRootOwnershipSurvivesPairingDrainAndRemoval
-count=1 -v` exits 0. The test covers queued, starting, running, idle, taken and
uncertain roots; exact refusal cause; immediate whole/enrollment/self disconnect;
removal; direct account disconnect; sibling enrollment isolation; and retention
of enrollment/account/key through repeated drain until persisted release.
Released roots and worker sidecars do not retain the daemon after release.

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

NOT RUN: full I1 combined fake-native smoke and its negative control, because the
full runtime composition is incomplete. No browser check is applicable. No
push to origin, deployment, review gate, real model call, daemon restart or live
pilot was performed. The approved remote runner's temporary test refs are not
origin pushes. Progress remains below 100 with the native claim/root-authority
dependency files named BLOCKED.

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
