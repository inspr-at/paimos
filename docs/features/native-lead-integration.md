# Native LEAD integration

## Current status — AEON-1346 slice A

End-to-end native sessions do not work yet. No host runs one. Production
server and daemon defaults stay off, and nothing in this branch turns them on.

This branch is **slice A** of AEON-1346. Slice A ships only:

- consent facts in migrations 1386 and 1387
- AEON-29 person approvals for `harness.control.native.session` and
  `harness.control.native.retention`
- production receipt authority (`ReceiptTx`, `ReadTx`, `FinalTx`) against the
  current grant and those facts
- immutable, non-renewable final-drain grants

**Slice B** is a separate ticket and is not started here. Slice B adds
production composition of the C0 Store, Services, Host and Launcher; daemon
`StartLocal`; the A5 start-to-confirmed-exit path with those ports installed;
and the `cmd/aeon-agentd` serve option.

Earlier sections record round history. A sentence that consent or final-drain
authorities were absent describes that earlier handoff. It is not the current
slice A result, and it is not a claim that a native session runs end to end.

## Round 5 handoff (AEON-1290), historical

I1 was incomplete at this handoff. Round 5 implements the granted final-publication,
exact attach GET and C0 CLI boundaries. The paired daemon remains the attributed
sender within its unchanged runtime ceiling; the ORIGINAL actor's current
control/chat rights and native grant are separate. Production remains disabled.
At the Round 5 handoff, native-consent and final-drain authorities were still
absent; slice A later added them. Production Store, Host, Launcher and
supervisor composition stayed absent and is now slice B. No live LEAD
handover, vendor call, daemon restart, origin push or deployment was performed.

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

## Round 5 boundary changes

`FinalAuthority` now carries the exact authenticated paired `Sender` and
separate `OriginalActor`. The repository rejects another sender or invented
sender scopes, declares an original delegated key before resource fencing, and
bounds that preflight and the final write to three seconds. The chat helper
checks the daemon only for `harness.worker`, the original actor for current
`harness.control`, `chat.receive` and `chat.send`, and the receiving person for
current chat/project visibility in the final transaction. A coordinator's
original person must resolve to the canonical conversation owner. Exact drain,
retention, completed-input/turn, context, privacy, deadline and idempotency checks
remain mandatory; ordinary final routes retain their stopped-lease refusal.

Receipt and stream GETs both send the attach's pinned runtime epoch, harness ID,
binding revision and integer session generation. The strict local query fence
is unchanged. A stale receipt GET closes local controller authority and keeps
its exact `wrong_generation` refusal. The socket fixture now uses the production
query validator, so missing coordinates cannot pass unnoticed.

`session start --profile native` now sends the frozen C0 `NativeLocalStart` and
`NativeLocalEnvelope`, returning a validated `NativeOperationResult` and honest
accepted/queued/uncertain outcomes. It requires `--intent FILE`: bounded,
owner-only C0 local-start JSON selecting an **already persisted person-issued
server intent**. The CLI neither issues that approval nor invents an actor,
policy, account or request ID. Existing `--cwd`, `--resume` and `--after-exit`
flags must match the artifact exactly, including its complete kernel exit
barrier. The protocol uses the repository's `nativecontract.LocalProtocol`
constant (`aeon-native-session/1`); no frozen contract was renamed.
Legacy attribution and attachment commands keep their separate contracts.

The protected local start handler now decodes C0 exclusively. An installed
`StartLocal` callback must resolve the exact persisted original person and
current grant before using the owning supervisor. Enabled calls also re-observe
the kernel peer; missing qualification refuses, and the old P1a envelope cannot
act as a fallback. Local body reads have an explicit server deadline. No callback
is installed by production `servePairedContext`, so this remains unavailable.

`nativeintegration.Delivery` composes an **already owned** C0 transport with the
P3 input/control journal, durable receipt reporter and explicit-final sink. It
serializes bounded operations, revalidates write authority before native I/O,
retains only separately approved explicit finals after exact completion, and
reports errors alongside real outcomes. Graceful close requires the exact
process digest. Root/group exit and a persisted exit receipt precede the release
callback; an uncertain exit retains the hold, and a qualified exit observer can
settle/retry without another close. Viewer detach has no process-release path.
This component does not launch/adopt a process or itself provide a native grant.

## Remaining production dependencies

AEON-1346 slice A adds the consent approval, the facts schema and the current
transaction authorities. It does not make an end-to-end native session work.
Runtime composition, daemon `StartLocal`, the A5 start-to-exit path and the
`cmd/aeon-agentd` serve option are **slice B** and are not started on this
branch. The explicit daemon serve flag also reaches the separately owned
command parser described below:

| Boundary | Remaining work and safety requirement |
| --- | --- |
| Current native consent source; `internal/nativesessions/consent`, `internal/nativesessions/store/types.go`, `internal/agentaccounts/native_session.go` | AEON-1346 implements `consent.Authority` for Store authorization and `RequireNativeGrantTx`. The exact `harness.control.native.session` approval anchors immutable facts in migration 1386; only a current person session owning the paired computer/account can approve. Missing or denied approval, revocation, stale revision, another person, changed policy and another computer all fail closed. The existing paired-start regression now uses actual approvals and the production authority. `RequireLocalProfileTx` additionally compares the resolved physical workspace root and cwd to consent; production launch/input composition must install that check. These ports are not enabled on a host. |
| `internal/nativeintegration/pilot.go`, `cmd/aeon/serve.go` | **Slice B.** Compose the real start Store with command/control, receipt, private daemon-proof, relay and final-drain authorities into C0 `Store`/`Services`. The start store alone lacks the complete command/report interface. The default-off constructor and the owned delivery component are present; production ports remain uninstalled. |
| `internal/agentd/native_session.go`, `native_transport.go`, `adapters.go`, `claudeassets/bridge.mjs`, `cmd/aeon-agentd/paired_serve.go` | **Slice B.** Install a qualified person-issued C0 Host/Launcher, daemon `StartLocal` and exact native input/turn/event/final adapter, plus the `cmd/aeon-agentd` serve option. The existing core still uses normalized legacy chat relay callbacks; those cannot be borrowed by the issued daemon key for native final publication. SDK input correlation is retained, but no full C0 provider-event/current-final publisher is installed. Exact native evidence must not be inferred from token text. |
| `internal/nativesessions/receipts/current.go`, `repository.go`, `final.go`, `internal/nativeintegration/pilot.go` | AEON-1346 implements production `CurrentAuthority` for `ReceiptTx`/`ReadTx`/`FinalTx`: original person/current grant, exact binding, issued pairing key, enrollment, generation, worker-lease digest, original operation metadata and exact provider input/turn/exit evidence. Migration 1387 adds immutable content-free operation metadata and non-renewable final-drain grants. Finals independently re-read a separate `harness.control.native.retention` approval. The real daemon proof exporter and command/drain registration callbacks still need composition; `WithRuntimeProof` is private in-memory authority, never a public-body substitute. |
| `cmd/aeon/openapi_test.go` | The full fresh-checkout command suite needs its generated OpenAPI prerequisite prepared before tests; the file is outside this write set. The generated bundle passes its own parity check; current full static is refused by the night protocol, and the standalone command suite is not reported green. |
| `internal/nativesessions/start/start.go`, `internal/agentd/native_after_exit.go`, `native_session.go`, `internal/nativeintegration/pilot.go` | **Slice B.** Connect the existing pinned source-exit/one-writer mechanism to C0's resume/after-exit fields and a fresh current claim, including daemon `StartLocal` and the A5 start-to-confirmed-exit path. The owning first-start helper still refuses resume/after-exit; its composition remains pending and must retain exact source identity and confirmed group exit. CLI serialization alone is not self-handover proof. |

The delivery smoke begins with a real managed-coordinator database fixture
and a fake owned transport. It exercises one delivery section with that fake
transport. It is not an end-to-end native session, and it does not install the
production first-start Store, Host, Launcher or the P1a SDK/supervisor path.
The A5 start-to-exit smoke is slice B.

## AEON-1346 slice A — consent, receipts and drain grants

The native scope names are `harness.control.native.session` and
`harness.control.native.retention`, both refinements of the existing
`harness.control` key ceiling. No permission or runtime-key ceiling is added.
An agent proposes for itself through AEON-29. The server appends all choices to
`rationale`, so the existing Desk/phone card shows the facts without a new UI or
changing the pinned `Approval` response. The additive proposal schema is
`NativeConsentApprovalProposal` in `api/areas/approvals.yaml`.

Migration **1386** adds nullable `approval_requests.native_consent` and the
RLS table `native_session_consents`. The facts columns are `computer_id`,
`account_id`, `workspace_id`, `workspace_root`, `model_profile_id`, `tool_set`,
`settings_profile`, `local_history`, `retain_finals`, `revision`,
`maximum_runtime_seconds`, `maximum_turns` and `maximum_tokens`; the anchor and
attribution columns are `tenant_id`, `grant_id`, `owner_person_id`,
`agent_principal_id`, `project_id` and `created_at`. A grant trigger copies the
immutable proposal only after a person-approved decision, in the same
transaction as the grant and `approval.approved`; denial never creates facts.
A second database guard refuses agent native decisions even under the existing
owner-workstation exception. Facts are immutable; changing them requires a new
approval ID. Current reads lock and re-read the live grant rather than trusting
the intent's policy or revision.

Migration **1387** is additionally needed because receipt projections cannot be
their own command/publication authority. `native_session_operations` stores
bounded, immutable original-operation metadata, with no input text.
`native_session_drain_grants` pins the exact binding, original consent revision,
separate retention approval, private chat target, deadline and final/byte caps.
Issuing the same target/retention context returns the existing window. These
expansions enable no runtime and retain no worker proof or final text.

Slice A does not finish the first build. **Slice B** adds production
composition, not more consent invention: the real current workspace check, C0
Host/Launcher and daemon `StartLocal`, the private receipt proof exporter,
command/drain registration, the exact provider final adapter, the A5
start-to-confirmed-exit smoke with those ports, and the `cmd/aeon-agentd`
serve option. `NativeLocalStart` contains the original start request but no
operation ID, while claim selects the persisted operation ID and the pending
command contract excludes starts; an owning resolver must bridge this without
impersonating the person or enlarging the issued daemon key. The serve option
needs `cmd/aeon-agentd/main.go`, which owns the serve flag parser and rejects
paired runtime overrides. No flag, environment switch, host restart or
permissive fallback was introduced. Both existing production defaults remain
off, and automatic LeadRuntime is unchanged. No end-to-end native session
works on this branch.

Verification at code commit `838d086bae`: guarded `remote-test.sh` with
`-count=1` exits **0** for `internal/nativesessions/...`,
`internal/nativeintegration/...`, `internal/agentaccounts/...`,
`internal/approvals/...`, `internal/agentd/...`, `cmd/aeon-agentd/...`,
`internal/reportercontract/...` and `internal/dsar/...`. This includes the new
consent and production receipt/final regressions, the strengthened paired-start
negative cases, the prior delivery smoke and the existing default-off refusal.
No vendor or other model CLI was run. The historical removed-guard
snapshot evidence remains historical; no new guard-removal snapshots or live
pilot qualification are claimed.

The migration guard passes for 375 migrations, including 1386/1387; its 43 tests,
the 33 ownership-audit tests, all 358 tier-tool tests and generated OpenAPI
parity pass. Guarded `ci-static-remote.sh` exits **3** with
`remote static check paused at night (night protocol: mbp2606 is shared with the CI pool); rely on the PR's hosted CI static job`.
Full static is therefore **unproven**, not green; no heavy local replacement was
run. Earlier remote attempts caught test-fixture and import-cycle errors, which
were corrected before the passing affected run.

Finishing round, local 23:12 CEST, on the slice A tree before this documentation
commit: `ci-static-locked.sh` exits **3** with the same night-protocol pause.
The script gates on the local hour (22:00–08:00), not on CI-pool file freshness,
so a two-minute wait cannot admit it. PR CI remains the static authority.
That refusal is not a passing static check, and it is not evidence that an
end-to-end native session works.

R0 additions in this round are exactly:

- DSAR classification: `approval_requests.native_consent`,
  `native_session_consents`, `native_session_operations`,
  `native_session_drain_grants` in `internal/dsar/inventory.json`.
- DSAR adapters: add `native_consent` to the existing `approval_requests` review
  allowlist and add one adapter each for those three new tables in
  `internal/dsar/adapter_inventory.json`.
- ESSENTIAL Go rows:
  `internal/approvals/TestNativeConsentRequiresCurrentPersonApproval` and
  `internal/nativesessions/receipts/TestProductionReceiptAuthorityRechecksConsentAndExactEvidence`.
  The existing paired-start test is strengthened in place. Existing native and
  approvals source globs cover ownership; no permission label, built-in agent
  exclusion, key-scope ceiling, new route, production fence-caller, web-tier or
  shard row is added.

## Acceptance handoff

| Item | Result |
| --- | --- |
| A1: C0 Host/Launcher, start Store, relay, receipts/final sink and attach wiring | SLICE A ONLY. Consent and receipt/drain transaction authorities are implemented. Production Store/Host/Launcher composition, private proof and command/drain issuance are slice B. This is not an end-to-end native session. |
| A2: person-invoked native path separate from automatic LeadRuntime | SLICE A ONLY. The real paired claim keeps original-person authority separate; C0 CLI/local start selects the persisted person-issued intent. Production `StartLocal` and supervisor ports are slice B and remain uninstalled; automatic LeadRuntime stays disabled. |
| A3: native roots in legacy account/host admission; exact root context | PASS AT THE IMPLEMENTED BOUNDARIES. Prior pairing/harness and guard-removal evidence is retained. The current paired-start regression now uses actual person native approvals and the production grant authority for original-control, native-grant, pairing and exact-issued-key refusals. No live-pilot qualification is claimed. |
| A4: default account, confined adapter and legacy CLI compatibility; inventories | PASS AT THE IMPLEMENTED BOUNDARIES. Prior CLI/attach compatibility evidence is retained. AEON-1346 adds two ESSENTIAL rows, two expand-only migrations and their DSAR rows; no role/key ceiling, permission or new route changes. Ownership and tier checks pass; current guarded static validation is refused by the night protocol and remains unproven. |
| A5: fake start/input/exact turn/explicit final/interrupt/confirmed exit; detach survives | NOT END TO END. Slice A adds a production receipt/final-authority regression and strengthens paired start with actual person consent. The older delivery smoke uses a fake transport and is not a native session on a host. Production first-start, daemon `StartLocal` and confirmed-exit composition are slice B. |
| A6: concrete disabled pilot for coordinator self-handover | SLICE A ONLY. Default-off production mount and refusal test pass. C0 intent/exit-barrier serialization is concrete and current grant ports are implemented. Installing those ports, the daemon serve option and C0 pinned after-exit launch is slice B. No live handover or enablement. |

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

## Round 5 verification and exact R0 handoff

PASS guarded remote at `d8d2ab16f`: the real pairing claim/root regression
passes all five cases, with the actual separate person capacity approval and
issued runtime key. After strengthening the bearer test to exercise real
production authentication as well as the handler's exact scope refusal, the
current regression passes again at `5bbe4f99a` and in the full store suite at
`a26497c52`. `/api/me` identifies the exact issued daemon; the production
pairing boundary rejects its control route for the exact runtime-authority
cause. The direct handler independently rejects its exact missing control scope.
No synthetic wider daemon role/key is used.

PASS guarded remote at `5bbe4f99a`: Native selections in store, receipts and
agentd pass. This includes
`TestNativePilotDeliveryPersistsFinalAndKeepsHoldThroughDetach`: exact C0 attach
and receipt GET, actual person input retained in the fake provider, durable
write/turn receipts, explicit final persisted through real chat storage with
daemon attribution/person receiver, wrong-input refusal, detach survival,
second exact-turn interrupt, uncertain group exit hold and confirmed exit
settlement. It uses injected provider/journal time and explicit native evidence;
no live provider/account was called. The fixture is not an end-to-end native
session, and native sessions do not work on a host.

PASS affected package results at `a26497c52`: full `-count=1` suites for
receipts, nativeintegration, nativeattach, CLI, native start store, agentd,
harness, reportercontract, agentaccounts, agentpairing and `cmd/aeon-agentd`.
The combined command exits 1 because `cmd/aeon/serve.go` retained an unused
native-module import from Round 4. That import is removed in `1cd79ec86`;
the overall failing command is not reported as green.

PASS final full receipts rerun at `65ed0527e` (4.254 s), including the narrowed
original-person chat-send revocation, exact sender/scope substitution denials,
explicit delivery smoke and the bounded delegated-key preflight. That same
fresh `cmd/aeon` run compiles and executes its server behaviors, but exits 1 on
`TestOpenAPISpecParses`: the guarded runner's fresh checkout has no git-ignored
`api/openapi.yaml`. This is an explicit test-preparation gap; it is not green and
no assertion was skipped/loosened. The canonical generator is used in static/CI;
a fresh-checkout preparation change to `cmd/aeon/openapi_test.go` would need a
test-file grant. The corrected binary compilation also passes in guarded static
Go vet. PR CI is authoritative.

PASS guarded `ci-static-locked.sh --merge-main` at `65ed0527e`: exit 0,
all **42 checks pass**, zero optional skips (164.74 s), including Go vet,
strict tiers, ownership, shared inventories, migrations, OpenAPI and web checks.
Earlier exit-3 static attempts were guarded capacity refusals. PR CI remains
authoritative. `manifests --check`, affected selection and `git diff --check`
also pass. Affected selection is FULL; it is selection, not a full-repository
executed Go suite.

Expected failing guarded negative controls use detached stash snapshots; none
is on the work branch:

| Removed wiring/guard | Snapshot | Observed intended failure |
| --- | --- | --- |
| P3 receipt-report wiring in Delivery | `0fb280a4b` | Explicit final remains pending with `input_outcome_unconfirmed` instead of persisted. |
| Original person's current control checks | `7af044c40` | `actor_control` unexpectedly returns nil instead of `permission_revoked`. |
| Current native grant checks, replaced by cached policy | `a3dcecedf` | `native_grant` unexpectedly returns nil instead of `permission_revoked`. |
| Exact issued pairing key pin | `d4c9db538` | `different_key` unexpectedly returns nil instead of `permission_revoked`. |
| Current pairing enrollment revocation checks | `ba4724aa7` | `pairing` reaches `quota_limited` instead of the exact `permission_revoked` guard. The regression rejects that wrong cause. |

Round 5 R0 adds exactly one ESSENTIAL Go row through the canonical tier writer:
`internal/nativesessions/receipts/TestNativePilotDeliveryPersistsFinalAndKeepsHoldThroughDetach`.
Existing tests are strengthened in place. Existing native source ownership
covers the code; no new production fence caller, route, migration, DSAR,
permission, role/key ceiling, web-tier or shard row is introduced.

At the Round 5 handoff, I1 remained below 100% for the production dependencies
listed above. Slice A later shipped consent facts, AEON-29 approvals, production
receipt authority and immutable drain grants, with nothing enabled on a host.
End-to-end native sessions still do not work. Production composition, daemon
`StartLocal`, the A5 start-to-exit path and the `cmd/aeon-agentd` serve option
are slice B. No origin push, deployment, model review gate, other model CLI,
live daemon restart, vendor call or LEAD switch was performed.
