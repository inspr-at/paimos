# Native session stream (AEON-1268, P2a)

This package supplies transient transport for one agentd-owned native binding.
Production wiring remains disabled in C0; I1/I2 connect these ports to the
qualified supervisor, authenticated owner socket, Store and final outbox. No
vendor binary, account, login state, legacy chat route or live daemon is changed.

`internal/nativestream.LocalAdapter` mounts every frozen C0 local route on the
existing owner-only Unix-socket mux. It requires `X-Aeon-Session-Protocol:
aeon-native-session/1`, strict method-specific envelopes, matching nested request
IDs and exact runtime/binding selectors. `LocalAuthority.Authenticate` must use
the existing bearer **and kernel peer** proof. `WithAccess` must independently
resolve the current binding and validate the server-issued operation grant,
viewer/client, consent, expiry, controller capability and any effect/step-up.
The bounded envelope is available to that authorizer as untrusted selectors.
The adapter does not open a network listener or create/read a credential file.
HTTP bodies use `httpapi.ReadRequestBody` with the frozen C0 size/time bounds.
The shared reader preserves an earlier caller deadline and closes a failed or
oversized read before decoding, grant checks or Host admission; failed reads
never clear the transport deadline or leave the connection reusable.
Control writes require `X-Aeon-Attach-Capability`; streams require the attach
result's nonsecret `X-Aeon-Viewer-ID`. Proofs never enter URLs, logs or frames.
Detach calls only Host.Detach. Host retains the final native-write/ownership
recheck; no database transaction may span native I/O.

`Outbound.Observe` accepts ordered observations from one trusted native reader
with the exact runtime epoch. Structured, Expert, receipt, approval and quota
projections share gapless source positions in that binding/generation.
`Outbound.Pump` uses the existing paired-daemon/worker proof to publish bounded
C0 batches through `HTTPPublisher`; redirects are refused, responses bounded
and acknowledgements checked against the exact binding. A transient HTTP
acknowledgement is not input delivery, final persistence or process exit evidence.
Its OperationResult carries the real current operation receipt; the adapter
does not invent a new receipt operation or change the frozen C0 enum.

`stream.Relay.Register` takes a trusted current Store projection. Publishers
cannot create a binding. `Relay.Publish` requires a paired private publisher
fence and independently validates/filters every frame before an atomic RAM
append. `Relay` implements C0 FrameSource through a request-scoped authenticated
`Viewer` context. `HTTPHandler.Stream` provides the authorized public SSE read
handler for later mounting. Its first event is a permission-filtered snapshot.
Agents receive structured events only; terminal viewer URLs, Expert and terminal
capabilities are stripped from their snapshots, including workstation agents.
Each view remains pinned to its exact runtime and never follows a successor.

Every session has one shared replay ring: at most 64 frames and 256 KiB, expired
after 120 seconds without a publication. Readers keep positions and one
unbuffered delivery slot, without content queues per subscriber. The registry
holds at most 128 current sessions and 128 viewers, with at most eight viewers
per logical owned session, including viewers draining from a replaced runtime.
The outbound ring uses the same bounds. Publication batches have at most 16
frames and 256 KiB. Source ordering is strict, and an exact retained retry is
idempotent; changed/reordered frames are rejected.

`Last-Event-ID`/`after` is the frozen C0 `<generation>:<seq>` position. It is never
authority: authenticated viewer grants independently bind caller, tenant,
session, runtime and view. A stale/evicted position fails before SSE headers with
409. Eviction during a stream emits `gap` then `end`; an API outage emits a gap
for lost outbound positions before the retained frames. A gap accounts for the
missing source positions and cannot imply complete replay. `Relay.Page` and
`HTTPHandler.LivePage` expose only bounded transient pages, with `truncated`
and bounded `before` cursors. The owning durable events handler separately reads
authorized history; these RAM pages contain no final answers.

All frame and keepalive delivery runs inside the current access-change fence.
Rechecking before queueing is insufficient. The fence must remain held through
the actual channel send or HTTP Write/Flush so revocation cannot race delivery.
Writes have a three-second deadline; streams rotate after five minutes and
recheck access every fifteen seconds while idle. Missing deadline support fails
closed. Revocation closes the viewer and emits only a content-free `end`, never
a queued frame. Reader disconnect and stream rotation do not stop the runtime.

Expert projection parses at most 1 MiB, depth 16, and rejects duplicate keys.
Compiled Claude/Codex event/field allowlists preserve native envelope casing.
Only qualified assistant text blocks/deltas may carry text. Credential/config
fields, reasoning blocks, tool argument/result bodies, arbitrary commands,
environments and native final-result text are omitted. Native correlations become
domain-separated opaque references. Arbitrary native key names and supplied
redaction paths cannot leak through omission metadata. Unknown providers/events,
malformed shapes, more than 64 redactions or oversized exports yield an empty
`frame_json`, `omitted: true` and safe metadata. ACP/Pi bodies remain omitted until
their provider qualification adds a tested projection. Tool summaries never
export raw bodies through the structured path.

`ExportPolicy.Text` must connect the existing authorized output policy. Mandatory
credential-shaped text filtering additionally runs before and after that policy;
missing qualification omits Expert text and refuses structured text export.
Terminal frames require an explicit qualified screen-export policy, owner watch
consent, taken mode and the exact terminal runtime epoch. Login/auth PTYs remain
ineligible. This boundary does not claim filesystem isolation or terminal renderer
safety; the separately qualified read-only renderer owns escape/device-reply
handling. No terminal input or resize can come from a web viewer.

Only explicit `ProviderEvent.Final` values reach C0 `FinalSink.Persist`, with
exact binding and validated final IDs. Sink refusal/rejection/expiry is surfaced.
No token assembly, raw transcript import or final text enters live replay. P3a
owns final-drain authorization, durable retry, retention and current-permission
rechecks; this transport adds no durable store and preserves legacy chat receipts.
The local `Host.EventsPage` adapter separately permits already-authorized retained
`assistant.final` results, rechecking the current read grant and output policy.
That durable-history exception cannot enter publication, SSE or the RAM ring.

Acceptance verification uses fake ports only. The tests establish queued-frame
revocation and outbound/subscriber stalls with barriers; clocks control replay
expiry and keepalives, while timeouts only detect hangs. No real model account,
browser, review gate, release or live cutover is involved.

The opt-in `-args -native-negative-control` test flag reruns regression assertions
with temporary Go compiler overlays. Removing pre-publication expiry must fail
because expired content was retained. Replacing the local adapter with the
reviewed `140a6982` source must fail because a body read left its connection
reusable. Fixtures use `t.TempDir`; the real source and work branch remain valid,
with no throwaway commit or second worktree.

R0 handoff for this same PR:

- Assign `internal/nativestream/**` to audit slice S2 (`agent-runtime`). Existing
  `internal/nativesessions/**` already covers the server package.
- Register all following tests in `scripts/ci/go-test-tiers.json` as ESSENTIAL:
  `internal/nativestream`: `TestNativeExpertProjectionFiltersAndBounds`,
  `TestTransientRingReportsLossAndRejectsStaleReplay`,
  `TestLocalHostProtocolRequiresPeerGrantAndExactBinding`,
  `TestSSEFencesQueuedContentAndKeepalive`,
  `TestOutboundLossRemainsTransientAndFinalUsesSink`,
  `TestHTTPPublisherBoundsAndBindsAcknowledgement`.
- Register `internal/nativesessions/stream` tests as ESSENTIAL:
  `TestSlowSubscriberRevocationAndViewIsolation`,
  `TestLiveGapClosesAndBindingReplacementRefusesOldView`,
  `TestServerHTTPAuthenticatesViewerAndEmitsGapEnd`.
- There are no new public/local contract routes, permissions, migrations,
  database locks, DSAR columns, contract pins or web test/shard rows. I1/I2 own
  mounting the already-authored routes and adapting final publication receipts.
- Run the targeted packages through `remote-test.sh`, affected selection via
  `node scripts/test-tiers/affected.mjs --base origin/main`, and guarded
  `ci-static-locked.sh` after the serialized R0 registrations. Any refusal or
  incomplete result remains an explicit handoff; authoritative PR CI must pass.

Builder handoff, 2026-10-13:

- PASS — versioned C0 Host HTTP adapter and authenticated outbound/server relay:
  peer/grant/controller/binding rejection and bounded acknowledgement tests.
- PASS — one owned binding across structured, Expert and terminal views, with
  tenant isolation and agent denial of Expert content; legacy chat is untouched.
- PASS — RAM/count/byte/idle bounds, ordered replay, expired cursors, gap/end
  frames and access checks on frames/keepalives; explicit finals use FinalSink,
  and only authorized retained finals replay through Host.EventsPage.
- PASS — native envelope allowlists, opaque correlations, credential/reasoning/
  tool-body removal, duplicate/depth/size/redaction bounds and unknown omissions.
- PASS — slow-subscriber, API-outage and access-revocation barriers demonstrate
  bounded memory, visible loss and no queued delivery after revocation.
- PASS — both targeted Go packages with `-race` on the approved remote runner
  at `4477de1f8`;
  retention negative control with `-race -v -args -native-negative-control`
  passed on the approved remote runner at `380477742` (the buffer implementation
  is unchanged).
- PASS — affected-set selection exited 0. Its initial selection omitted these
  unregistered packages; R0 must register the nine tests and rerun selection.
- NOT GREEN — guarded `ci-static-locked.sh` exited 1: go-tiers, audit-tests and
  audit-coverage fail solely for the R0 registrations listed above. Thirty-nine
  other checks passed, with zero optional skips. Re-run after R0's same-PR edits.
- Local generated-bundle checking was unavailable because ignored
  `api/openapi.yaml` is absent. No authored contract changed; guarded static
  OpenAPI property/sort checks passed. Browser checks are inapplicable to P2a.

Only the three assigned write-set surfaces were edited. Shared inventories,
API sources, migrations, versions, live daemons and vendor logins are unchanged.
No other builder worktree was touched; no production deployment, origin push,
review gate or model CLI was run. I1/I2 own integration; R0 and authoritative
PR CI remain required before merge.

Fix round 2, 2026-10-13 (reviewed source `140a6982`, implementation `cb33a0b95`):

- PASS — the reported `TestHTTPReadDeadlineOwnership` failure reproduced on
  `140a6982` through `remote-test.sh`; the fixed adapter has no private HTTP
  read-deadline calls and uses the shared bounded body reader.
- PASS — `remote-test.sh` with `-race` for `internal/httpapi/...`,
  `internal/nativestream/...` and `internal/nativesessions/stream/...`.
  Regression subtests extend the existing registered-name handoff; they assert
  exact body-error responses, connection closure, an unchanged earlier caller
  deadline, no reset after failure, joined readers and zero Host admission.
- PASS — remote `-race -v -args -native-negative-control` for
  `internal/nativestream/...`. The actual adapter source from `140a6982` fails
  specifically for leaving a failed-body connection reusable; the existing
  idle-expiry mutation also fails for its claimed retained-content risk.
- PASS — offline `node scripts/test-tiers/affected.mjs --base origin/main`
  exited 0. It still cannot select the two unregistered native-stream packages;
  the nine ESSENTIAL test rows in the R0 handoff remain required.
- OPEN — the CI audit failures require the single S2 path
  `internal/nativestream/**` in `scripts/audit/slices.json`. Ownership lookup
  confirms all seven files in that directory are unassigned. The normative
  P2a plan reserves audit/tier metadata exclusively for R0; this fix round
  preserves that ownership and does not edit those shared files.
- NOT GREEN — guarded `ci-static-locked.sh` exited 1 at `cb33a0b95`:
  38 checks passed, with zero optional skips. Audit tests, audit coverage and
  Go tiers require the R0 entries above. The public-name guard additionally
  rejected operator host names in the earlier handoff; those references have
  been replaced with value-free remote-runner descriptions.
- NOT GREEN — the guarded static rerun at `523afa978` exited 1: 39 checks
  passed, with zero optional skips; public names now pass. The only failures
  are `go-tiers`, `audit-tests` and `audit-coverage`, all requiring the R0
  assignment/test rows already listed. R0 must apply those serialized entries
  on this branch in the same PR, then rerun affected selection and the guarded
  static check before the coordinator's review and authoritative PR CI.
- Browser/web-unit execution is inapplicable to this source-only fix. The C0
  contracts, route inventories, permissions, migrations and versions are
  unchanged. No origin push, deployment, review gate or model CLI was run.

Fix round 3, 2026-10-13 (source `8507a74eb`, then R0 merge `4978ebaf3`):

- PASS — selected PPM account and the coordinator's exact worker-session marker
  were verified on AEON-1268 before material work. Initial `origin/main`
  `06929d1a4` was already an ancestor. The later R0-only `3507e3319` was merged
  cleanly into this branch as `4978ebaf3`, without an origin push.
- PASS — guarded remote `-race` tests for `internal/httpapi/...`,
  `internal/nativestream/...` and `internal/nativesessions/stream/...` at
  `8507a74eb`. These retain the authenticated adapter/relay, shared binding/view
  isolation, bounded replay/FinalSink separation, native redaction and
  revocation acceptance checks established with barriers. The CI Go shards
  also passed; measurements report all nine stream tests passing as NIGHTLY.
- CONFIRMED — CI job `115121536245` failed only at Go tier validation: the nine
  test names listed in the R0 handoff are unclassified. CI job `115121536319`
  failed only the two audit assertions because all seven
  `internal/nativestream` files lacked a source ownership glob. Neither log
  reports a transport behavior failure requiring a new regression against
  `8507a74eb`.
- RESOLVED — the coordinator's audit PR #623 merged during this round. Its one
  additive S2 glob, `internal/native*/**`, is now inherited from `origin/main`.
  It covers all seven stream files and adds no runtime changes. Guarded static
  validation on `4978ebaf3` passes both `audit-tests` and `audit-coverage`; the
  existing audit assertions failed on the reviewed source and now pass.
- OPEN — the nine ESSENTIAL Go tier rows are still absent. The normative plan
  assigns `scripts/ci/go-test-tiers.json` solely to R0 and says builders never
  independently rewrite these inventories. R0 must apply the exact serialized
  registrations listed above in this same PR and rerun validation. The worker
  authored no shared inventory changes and changed no other slice's source.
- CONFIRMED — CI's `web-shard (2)` was cancelled in its selected-case step.
  The web aggregate failed as a consequence; tier measurements explicitly
  report `coverage: incomplete`, `missingEvidence: [web-shard-2]` and
  `measurementCauses: [missing_case_artifact]`, with no reported failed cases.
  The coordinator's CI run must obtain the missing shard evidence before merge.
  All four web-unit shards and the first browser shard passed on the reviewed
  source.
  This package has no web source, unit-test or browser-spec changes.
- PASS WITH LIMIT — offline affected selection against `origin/main` exits 0,
  but still omits `internal/nativestream` and `internal/nativesessions/stream`.
  Repeat it after R0 adds the tier rows; an exit-0 plan is not evidence that
  these unregistered packages were selected or tested.
- NOT GREEN — the final guarded `ci-static-locked.sh` on `4978ebaf3` returned
  exit 1: 41 checks passed, zero optional skips. Its only failure is `go-tiers`,
  listing exactly the nine R0 registrations above. Test-tier tests, web-shard
  tests, audit tests/coverage, Go vet, web typecheck/lint and public names pass.
  R0 must apply the tier rows and rerun affected selection and guarded static
  validation before the coordinator obtains complete authoritative PR CI.

This round records exact CI causes and renewed passing transport checks without
changing runtime behavior, adding tests that mirror the implementation, or
loosening any assertion. R0 metadata and complete authoritative CI evidence
remain required before the coordinator's review/merge. No origin push,
deployment, review gate, other model CLI, live runtime or vendor account change
was performed.

Fix round 4, 2026-10-13 (reviewed source `055f1be7a`):

- PASS — PPM / INSPR / workstation-agents and the coordinator-recorded builder
  marker `e55d92a9-5606-480a-ac99-e75745937e47` were verified before writes.
  Refreshed `origin/main` is still `3507e3319`, already an ancestor of HEAD;
  PR #621 is mergeable, so no additional merge is required.
- PASS — guarded remote `-race` tests for `internal/httpapi/...`,
  `internal/nativestream/...` and `internal/nativesessions/stream/...` on
  `055f1be7a`. These retain all five P2a acceptance checks: authenticated C0
  transport, binding/view isolation, bounded transient replay with separate
  FinalSink, native export filtering and barrier-established revocation.
- CONFIRMED — CI run `38348344344`, Go static job `115135260857`, fails only
  at `node scripts/test-tiers/cli.mjs check go --strict`, listing the same nine
  unclassified stream tests. Go vet, bootstrap and tier selector regressions
  pass. None of the nine required ESSENTIAL rows exists in the current manifest.
- CONFIRMED — the web aggregate job `115141679994` fails because
  `web-shard (2)` job `115135986852` was cancelled. Its first batch reports
  215 passing cases; its next batch reaches case 93 of 100 before
  `The operation was canceled`. There is no reported assertion failure to
  repair. All four web-unit shards passed. Tier measurements report
  `coverage: incomplete`, `missingEvidence: [web-shard-2]` and
  `measurementCauses: [missing_case_artifact]`. The coordinator must obtain
  complete shard evidence from authoritative PR CI; no browser claim is green
  merely because a partial batch passed.
- PASS WITH LIMIT — the required offline affected plan exits 0 with mode
  `essential`, layout `full` and 128 selected Go packages, but omits both
  `internal/nativestream` and `internal/nativesessions/stream`. Register the
  rows below before rerunning selection. Browser checks are inapplicable to
  this non-browser slice; the existing CI cancellation is an incomplete run.
- NOT GREEN — final guarded `ci-static-locked.sh` on `055f1be7a` exits 1:
  41 checks pass, zero optional skips. Only `go-tiers` fails, naming exactly
  the nine missing R0 rows. The mandatory tier selector, web-shard and audit
  self-checks pass, as do Go vet, web typecheck/lint and audit coverage.
  R0 must apply its rows and rerun affected selection and guarded static
  validation before complete authoritative PR CI and the coordinator's gate.

R0's concrete same-PR change is to add these nine rows to the `tests` array in
`scripts/ci/go-test-tiers.json`, retaining all existing rows and assertions:

```json
[
{"kind":"go","package":"internal/nativesessions/stream","name":"TestLiveGapClosesAndBindingReplacementRefusesOldView","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativesessions/stream","name":"TestServerHTTPAuthenticatesViewerAndEmitsGapEnd","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativesessions/stream","name":"TestSlowSubscriberRevocationAndViewIsolation","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativestream","name":"TestHTTPPublisherBoundsAndBindsAcknowledgement","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativestream","name":"TestLocalHostProtocolRequiresPeerGrantAndExactBinding","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativestream","name":"TestNativeExpertProjectionFiltersAndBounds","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativestream","name":"TestOutboundLossRemainsTransientAndFinalUsesSink","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativestream","name":"TestSSEFencesQueuedContentAndKeepalive","tier":"ESSENTIAL"},
{"kind":"go","package":"internal/nativestream","name":"TestTransientRingReportsLossAndRejectsStaleReplay","tier":"ESSENTIAL"}
]
```

The normative P2a brief says to write only its exclusive set and stop/report
when another file is required. The plan's R0 rule says builders never
independently rewrite shared inventories. This fix round therefore stops at
that ownership boundary: no test removal/rename, weakened assertion or runtime
change can correctly repair a missing manifest registration. There is no
transport behavior finding requiring a new regression against `055f1be7a`.
The required R0 change and complete CI evidence remain open. No authored shared
inventory, API, migration, version, origin push, deployment, review gate, model
CLI, live runtime or vendor account was changed.

Fix round 5, 2026-10-13 (reviewed source `8b4090d07`):

- CONFIRMED — CI run `38351733422` is the PR run for `8b4090d07`.
  Go static job `115145749619` fails only at strict Go tier validation,
  naming exactly the nine rows above. The failure is a missing registration,
  not a failed transport assertion. No behavior fix or new regression against
  `8b4090d07` is indicated by this review.
- PASS — the run's Go JSONL artifacts record all six `internal/nativestream`
  tests and all three `internal/nativesessions/stream` tests passing. The CI
  merge commit `08734bea7` changes neither stream package relative to the
  reviewed source. These tests retain all five acceptance checks: authenticated
  C0 transport, shared binding/view isolation, bounded transient replay with
  separate FinalSink, native export filtering and barrier-established revocation.
  Their measurement classification is still NIGHTLY; passing execution does
  not satisfy the required ESSENTIAL registration.
- CONFIRMED — web aggregate job `115154059022` fails because both browser
  shards were cancelled. Shard 1 completed a 100-case batch, then reached
  case 153 of 191; shard 2 completed a 215-case batch, then reached case
  95 of 100. Both report `The operation was canceled`, without an assertion
  failure. All four web-unit shards passed. Measurements report
  `coverage: incomplete`, both browser shards in `missingEvidence`, and
  `measurementCauses: [missing_case_artifact]`. Complete authoritative CI
  evidence is still required; partial batches do not establish a browser pass.
- CONFIRMED — refreshed `origin/main` is `fd8fae22f`. PR #621 is mergeable,
  so the brief's conflict-triggered merge is unnecessary. That main revision
  also lacks these nine stream rows; merging it cannot resolve this finding.
  The selected PPM / INSPR agent and coordinator-recorded builder marker were
  verified before the handoff edit.
- PASS WITH LIMIT — the offline affected plan exits 0 in `essential` mode
  with `full` layout and 128 selected Go packages, but omits both stream
  packages. Repeat selection after R0 adds the rows. This non-browser slice
  has no affected web unit tests or browser specs of its own.
- INCOMPLETE — two guarded remote `-race` attempts for `internal/httpapi`,
  `internal/nativestream` and `internal/nativesessions/stream`, and two guarded
  `ci-static-locked.sh` attempts, returned exit 3 because the remote slots/CI
  pool were full. No local fallback, guard bypass or green static claim was
  made. The current CI strict-tier failure remains independently confirmed.

R0 can apply the exact nine registrations through the existing classifier in
its serialized, guarded metadata lane:

```sh
node scripts/test-tiers/cli.mjs classify go --tier ESSENTIAL --only 'internal/nativestream:'
node scripts/test-tiers/cli.mjs classify go --tier ESSENTIAL --only 'internal/nativesessions/stream:'
node scripts/test-tiers/cli.mjs manifests --write
```

The filters are literal package/test identity prefixes and the classifier adds
only undeclared tests. They select the six and three rows already listed above,
preserving every existing tier and assertion. R0 must apply them on this same
branch, rerun affected selection and guarded static checks, then the coordinator
must obtain complete PR CI evidence before another release decision.

The exclusive P2a write set and R0 ownership rule still prohibit this builder
from editing `scripts/ci/go-test-tiers.json`. This round therefore hands off the
remaining blocking metadata change and cancelled CI evidence explicitly. No
runtime behavior, test assertion, shared inventory, API, migration or version
was changed; no origin push, deployment, review gate, model CLI, live runtime or
vendor account action was performed.

Fix round 6, 2026-10-13 (reviewed source `eee2be772`):

- PASS — approved remote `-race -count=1` tests for `internal/httpapi/...`,
  `internal/nativestream/...` and `internal/nativesessions/stream/...` on the
  reviewed source. The authenticated C0 transport, binding/view isolation,
  bounded replay and separate FinalSink, native export filtering, and
  barrier-established revocation acceptance checks remain passing.
- CONFIRMED — job `115157764922` fails only strict Go tier validation for the
  same nine stream tests in the R0 handoff. Downloaded Go artifacts from CI run
  `38355592739` show all nine passing and no failed cases; they still default
  to NIGHTLY. No runtime behavior failure or assertion change is indicated.
- BLOCKED — refreshed `origin/main` is `fd8fae22f`, already an ancestor of
  this branch, and both manifests still contain zero stream test rows. The
  normative plan gives `scripts/ci/go-test-tiers.json` exclusively to R0:
  "Builders send exact row/registration intents; they never independently
  rewrite these shared inventories." The P2a write set remains the two stream
  packages and this document. R0 must apply the nine ESSENTIAL rows using the
  classifier commands above on this same branch before final validation.
- CONFIRMED — web aggregate `115164323793` fails because browser shard 2 was
  cancelled during its selected-case step. Its first batch reports 215 passed;
  its next batch ends with `The operation was canceled`, without a reported
  assertion failure. All four unit shards and browser shard 1 passed. The
  measurement aggregate reports `coverage: incomplete`,
  `missingEvidence: [web-shard-2]` and
  `measurementCauses: [missing_case_artifact]`. The coordinator must obtain
  complete CI evidence; this package has no web source, unit-test or browser-spec
  changes.
- PASS WITH LIMIT — offline affected selection exits 0 with 130 Go packages,
  but still omits both stream packages. Re-run after R0 registration; this
  exit-0 result does not establish stream-test selection.
- INCOMPLETE — both guarded `ci-static-locked.sh` attempts return exit 3
  because both remote slots are occupied. No local fallback or guard bypass
  was used, and the independent CI tier failure remains blocking.

Only this in-scope handoff document changed. No behavior fix was needed, so
there is no new regression assertion or negative-control mutation against
`eee2be772`. R0 registration, guarded static validation and complete PR CI
remain required. No origin push, deployment, review gate, model CLI, live
runtime or vendor account action was performed.
