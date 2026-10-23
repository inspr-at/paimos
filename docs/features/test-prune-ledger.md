# Test-prune risk and budget ledger

AEON-1673 implements P01 of AEON-1668 / GUI-37 under epic AEON-1670. It adds planning data and a lineage check. Existing tests, tiers, browser launch records, CI flags, required checks and historical replay evidence retain their behavior.

The person-approved retirement/demotion note is Markus's standing approval of the entire prune program on **2026-10-22 at 22:15 Europe/Vienna**, recorded by the coordinator in the AEON-1673 brief and attributed to **AEON-1670**. That approval covers the planned removals of redundant expectations. Every unique safety risk still needs its concrete replacement and proof before a cutting package removes the original. Tick's review of the visual states and immutable references is a separate prerequisite, currently pending.

`scripts/test-prune/baseline.json` freezes native registration identities, tiers, tags, post-audit catalogue counts, assembled per-spec launch policy, timing metadata, and SHA-256 pins for their inputs and the historical replay evidence. Its source is commit `78200ff4b01f9b93e001bc943953a67af9caa7a1`; this is a refresh of the committed registration catalogue, without browser execution or fresh native enumeration. The source audit used `52814e65e62a9888525f1fcec3aeb7617685b33b`.

The refreshed browser catalogue is **5,318 cases across 378 owners**, with **1,169 ESSENTIAL, 1,421 GATED-FULL and 2,728 NIGHTLY**. Existing full selection includes **3,092** browser cases. Compared with the audit's 5,316, `ui-audit-rules` and `start-agent` each gained one registration. There are also 963 Node and 3,272 Vitest registrations; the seven separately configured e2e registrations remain outside the proposed browser budget. Timing estimates remain those of the source audit, rather than newly measured savings.

`scripts/test-prune/risk-ledger.json` contains all 378 spec budgets, the exact logical core **C01–C29**, and 375 destination records. The target stays **338 = 256 functional + 2 cold request-bound + 80 visual** cases; core cases are included in those owner budgets. Native bindings of future consolidated core cases remain explicitly pending implementation.

The **647 protected lineage rows** cover all 455 historical drift cases (451 browser and four runtime), six Go drift obligations, all 31 mixed delete-candidate groups and all 155 currently tagged browser cases. Generic `delete-candidate` tags on Node and Vitest registrations are outside this browser-prune candidate population; explicit runtime drift obligations remain protected. The groups' 308 historically reported cases are metadata, not the current tagged population. All 25 unresolved drift titles retain their original text and current whole-file fallback identities. Each risk points to retained behavior, a named runtime suite, a visual state, or approved retirement with a surviving destination. The three new runtime recipients (`ui-audit-rules`, `native-program` and `native-diagnostics`) are recorded as planned; naming a recipient does not claim equivalence has been proven.

Run the lightweight check with:

```sh
node scripts/test-prune/check.mjs
```

The check derives required risks from the frozen source independently of the editable mappings. Missing rows, empty or unknown destinations, altered historical fallback identities, duplicate core/state identities and budget mismatches fail with exit 1. The one-time `node scripts/test-prune/check.mjs --p01` acceptance check additionally rejects removal, demotion or modification of existing catalogue rows and changes to pinned browser sources, launch/configuration/host/build/aggregate policy or historical evidence. This snapshot comparison is explicit so future assertion additions or timing refreshes do not encounter a permanent P01 source freeze. The newly added ESSENTIAL Node test is an additive registration made through the canonical tier writer.

For later phases, an actual removed or demoted protected registration must have a verified replacement with evidence and current native identities. Such a protected cut also requires Tick-approved references. Default mode derives `cuts` only from protected-risk anchors, so an unrelated unit rename or removal does not require visual approval. The explicit `--p01` snapshot check still freezes the whole catalogue, including unprotected units. Updating `phase` alone cannot permit an unverified cut. Current native collection, canonical manifests and current drift-obligation remapping remain the cutting package's responsibility; the frozen original obligations and historical replay records remain intact. The live `drift-obligations.json` may be deliberately remapped by a cutting package, while its original contents remain in the baseline.

The behavior test is `web/tests/test-prune-ledger.test.ts`, also imported by the existing static tier-policy test entry point. Its negative-control fixture, `scripts/test-prune/testdata/missing-destination.json`, removes the destination and current registration of the protected late ticket lookup after a person/workspace switch. The check must report that exact missing destination. The test also rejects deletion of entire lineage rows, dangling destinations, premature cuts/demotions, policy drift and incomplete visual checkpoints. It distinguishes an unrelated unit rename/removal, which passes default mode, from a protected anchor rename, which fails with its exact risk and destination; `--p01` still rejects the unrelated rename/removal.

`scripts/test-prune/visual-manifest.json` is ready for Tick's reference review: **18 independently owned states and 80 explicit checkpoints**, using 390×844 and 1440×900 in both themes, plus 1024×768 for Agents, Decision Desk, Account use matrix and Quote editor. It includes the three changes from Tick's 2026-10-22 section-8 addendum:

- `ticket-workspace` replaces Theme editor: long title and key, properties panel, attachment strip with one preview, checklist focus ring and open chat dock.
- `start-agent` replaces Release history: Run once mode, filled task, “Create a ticket in a project”, read-only and a folded “Runs on …” row with a capacity warning.
- Goals has separate `fixturesByProfile`: desktop Expert with one expanded goal, pace badges, a late Lands with “at this pace”, numbered steps with stage tracks and a waits marker; phone Simple with 56 px rows and a candy bar frozen by reduced motion. Both retain the long title/key fixture.

The two swapped destinations are remapped throughout spec budgets, protected-risk lineage and surviving retirement destinations. The manifest requires actual image comparison, axe, readiness/font barriers, reduced motion and clipping/overlap/console checks. Interaction and ±0.5 px control-stability proof remain separate.

The proposed reference authority is an existing authorized **PPM / INSPR attachment on AEON-1670**, retained for the lifetime of the visual set. Tick approval, attachment identity, archive digest, checkpoint member digests and a successful availability verification are explicitly unset. P02 must supply and verify those values before cuts. CI captures stay in the existing artifact flow under `testInfo.outputPath()`; PNGs are never committed. An unavailable, expired or mismatched reference fails and requires a newly reviewed reference. The current run cannot become its own baseline, and automatic rewriting is forbidden. This package captures or publishes no baseline and introduces no credential.

New source ownership uses the existing `scripts/**` S7 and `web/tests/**` S9 slice rules; no additional slice pattern is necessary.

## P07 runtime preparation — AEON-1679

P07 is **incomplete**. Its browser registrations remain **451 → 451** across 33 owners, against the planned ceiling of 27. No browser case, historical drift identity, launch record, tier or visual expectation has been removed or demoted. P01's visual approval is still `pending`, its baseline authority is `pending-review`, and its attachment/digest pins are unset. `check.mjs` deliberately requires that authority before a protected cut; this package preserves that requirement. The original reader-drift negative control also remains until the retained browser journey can be proven. Session rows, composer/reader geometry, terminal side effects, exact-session controls, receipts, approval and recovery journeys all retain their original browser coverage.

The executable prerequisites added here are:

- `native-program.unit.test.ts`: six cases execute the unchanged context/response/stream boundary extracted from `SessionPanel.vue` into `nativeSessionProgram.ts`. They preserve the original malformed JSON, `null`, missing/invalid capability, replay `[null]`, invalid grant and invalid SSE-envelope vectors. They also check byte/UTF-8 limits, reader cleanup, view-only attach, exact binding/query/header/detach identities and cancellation before a late stream chunk. No native control port is added.
- `native-diagnostics.unit.test.ts`: six cases execute production binding/privacy/shape/uint64 admission, exclusive replay cursors, count/byte bounds, deduplication/eviction, coalescing/correlations and view/consent derivation. Every original quota vector from `native-expert-stream.spec.ts` is retained. Browser revocation, origin enforcement, physical scrolling and terminal side effects remain separate obligations.
- `resilient-polling.unit.test.ts`: four added cases use held promises and fake time for shared reads, stale-screen rejection, hidden/offline polling, wake recovery and shared event-burst catch-up. Existing GET retry checks now advance fake timers. `stream-health.unit.test.ts` adds a person-reset case that replaces the shared stream and rejects callbacks from the previous source.

All **26 cases** in these four targeted runtime suites passed locally, one file and one worker at a time after remote admission refused. The **17 added native registrations** are ESSENTIAL, classified through the canonical tier writer using the targeted native Vitest inventory. Existing registrations and tiers are preserved. The new module and fixture are covered by the existing `web/src/**` and `web/tests/**` S9 ownership rules, so no slice rule is added. The three P07 runtime destinations now bind exact verified native identities; retained browser destinations remain planned. The shared P04 stream-health destination is not marked complete on another package's behalf.

Seven temporary negative controls, run against saved positive production code without a negative-control commit, failed for their intended assertions. The initial six used positive-state stash `6d3a28675a07211ed17b66736230c5556334a3c7`:

| Disabled production guard | Rejecting runtime case | Observed result |
|---|---|---|
| Required capability booleans | native-program: malformed context | Accepted invalid capability; assertion failed |
| Attached viewer binding | native-program: malformed/controlling grant | Invalid grant resolved; rejection assertion failed |
| Diagnostic event binding | native-diagnostics: binding dimensions | Foreign tenant frame admitted; assertion failed |
| Diagnostic private-field filter | native-diagnostics: expert privacy | Synthetic private field admitted; assertion failed |
| Poll generation fence | resilient-polling: shared current read | Late old-screen response applied; assertion failed |
| Shared stream source identity | stream-health: person reset | Old-person callback dispatched; assertion failed |
| Replay page count | native-diagnostics: descending replay | Otherwise valid descending 501-frame page admitted; assertion failed |

All mutants were restored, and the four complete targeted suites passed again. No timeout, compilation error or unrelated failure is counted as a successful negative control.

The measured local resilient-polling invocation was **1.23 s for four cases before → 201 ms for eight cases after** (one observation each, including runner startup; not a performance assertion). Final individual process walls for the four suites were approximately 0.35–0.38 s each. **Browser runner-time change is unmeasured**, with no browser saving claimed. LEAD `remote-spec.sh` refused with `remote busy (host guard); wait or rely on PR CI`; `ci-static-remote.sh` refused with `remote static check paused at night (night protocol: mbp2606 is shared with the CI pool); rely on the PR's hosted CI static job`. No heavy local substitute was run.

Remaining work is the proven 27-case browser consolidation, all old-case mappings/current obligation updates and cuts after P02 supplies approved immutable references, plus affected browser execution and `ci-static --merge-main` through the authorized runner or PR CI. AEON-1662 is not declared closed: its mounted polling synchronization and browser proof are still required. The ledger check reports `cuts: false`; neither the absent visual authority nor refused runner capacity is reported as acceptance.

The final main reconciliation included `ba983ea03`; the tier merge was clean. Canonical manifest validation, the ledger check and its Node regression, the SessionPanel SFC script compile and targeted ESLint check passed. Final browser and static attempts repeated the refusal messages above. Full static/typecheck and affected browser execution remain unproven.

Outside the browser-spec/runtime-test paths: `web/src/components/agents/SessionPanel.vue` imports its original boundary functions from `web/src/lib/nativeSessionProgram.ts`; this minimal extraction makes the replacement execute the real production validation and transport rather than a test copy. Both retain the same request bounds, view-only authority, cancellation, error handling and detach behavior. No production version, contract, migration, hosted setting or application UI behavior changes.
