# P11 unprotected cuts and replacement evidence

AEON-1683 cut round A removes **79 unprotected browser registrations: 465 → 386 across the P11 package**. The 19 changed owners account for 239 → 160 registrations. These cuts consolidate identical viewport/theme/locale copies and screenshot tours into existing browser cases with their assertions intact. They do not transfer unique behavior to an unexecuted runtime suite. The full P11 ceiling of 22 remains a later program objective; this round is limited to the unprotected cuts authorized in the coordinator's 2026-10-23 cutA brief.

Main was merged first at `2f26947bb`. The Delivery lane conflict retains the phone replacement and all eight new AEON-1656 cases. Main's seven new update-toast cases retain their unique count/geometry journey and both phone wrapping and desktop spacing profiles; four repeated profiles have exact destinations. Across both P11 rounds, the current input is 508 → 386 registrations (the frozen P01 input excluding subsequently added main cases is 493 → 375).

`scripts/test-prune/p11-execution.json` maps all 300 frozen registrations in the 24 changed owners, including the first round's five owners. The other 16 owners map their frozen registrations to themselves. `cutA.mappings` separately accounts for all 79 current-round cuts, including the four new-main toast copies absent from the P01 baseline. The proof pins the before commit and manifest digest. All **194 protected browser anchors in 17 owners** keep their original native identities, tiers and assertions. The standing default check reports `cuts:false`: its cut flag measures protected anchors, rather than every removed registration. P02 references remain pending and are not bypassed.

| Current-round owner | Before | After | Retained browser risk |
|---|---:|---:|---|
| delivery-arion-v5 | 14 | 7 | Exact counts, missing/partial honesty, changing-window stability, phone targets in both renderers |
| delivery-numbers | 14 | 6 | Scope/view/window behavior, partial results, phone and desktop inline Settings/navigation geometry |
| delivery-polish | 18 | 12 | Both contrast themes at both layouts, native keys, pinned Learn, touch targets and stable controls |
| delivery-simple | 11 | 6 | Failed saves/reads and retries, phone targets, desktop tooltip, stationary Learn and selectors |
| status-time | 18 | 11 | Lifeline visits/reopen/retry, native hover/keys/tap, full marker text, Tickets Simple/Expert, live/reduced motion |
| portal-releases | 5 | 3 | Public frozen notes, hidden-data exclusion, language persistence, reveal and catalogue navigation |
| release-groups | 10 | 9 | Protected desktop grouping is the assertion superset of the removed phone copy; native disclosure/search remain |
| release-own-tenant | 4 | 2 | Independent tenant isolation, frozen wording, language switches and filters in both list layouts |
| release-tenant-notes | 20 | 10 | Both mocked tenant shapes and every phone entry, plus desktop's distinct direct-page preselection |
| usage-accounts | 9 | 7 | Both layouts, incomplete inventory, retained refresh failure, queued verification and protected pairing 403 |
| webcore-recovery | 9 | 5 | Phone sheet/desktop typing, retained failed timing save, stale knowledge and bounded access history |
| aeon-635 | 21 | 15 | Coarse-pointer copy in both layouts, waiting keys/focus, partial/pagination honesty, every existing header breakpoint |
| policies-rules | 9 | 5 | Admin/member permissions, mobile touch targets, risk-group stability, native rule-link focus |
| portal-public | 13 | 12 | Catalogue/vote on the narrow profile; all wish/correction/default/policy binding refusals remain |
| portal-roadmap | 3 | 2 | Public roadmap projection, hidden-data exclusion, language/reload/navigation and unavailable states |
| release-historic | 7 | 6 | All protected backfilled profiles; presentation behavior remains in its protected owner |
| release-switches | 17 | 16 | Highlights/Details, EN/DE, address/reload/Back, native radios, both-layout/both-theme axe |
| release-update-notice | 23 | 13 | Every notice outcome, both narrow clipping fixtures, toast count/hover stability and both wrapping layouts |
| reserved-versions | 14 | 13 | Protected row layouts/counts, direct-link keyboard navigation, failed opt-in save and hidden comparison scopes |
| **Changed owners** | **239** | **160** | **79 redundant registrations removed** |

The source changes reduce registration loops or remove redundant artifact tours. They do not weaken assertions in surviving journeys or move removed cases into NIGHTLY. The webcore screenshot now uses `testInfo.outputPath()`. Tier rows and each affected shard source's `listedTests` were updated through the canonical manifest writer; historical timing samples and their original denominators remain historical measurements. No runtime donor is removed merely because a similarly named unit file exists.

The ledger behavior test now constructs a valid, isolated P01 snapshot before attempting an unrelated rename/removal. After P11 advanced the working phase, the old counterexample could fail at the phase guard instead of testing the intended removal. The positive snapshot assertion and exact expected removal error preserve that distinction. P11 checks also derive current-round removals from frozen plus new-main identities and reject a missing new-main mapping.

## Protected, after P02

These owners retain every protected anchor. Pinned visual references alone will not establish runtime equivalence; each later cut still needs its concrete replacement proof.

| Owner | Protected cases |
|---|---:|
| aeon-635 | 6 |
| delivery-rating | 3 |
| deploy-target | 1 |
| footer-release-pill | 42 |
| release-codenames | 24 |
| release-groups | 5 |
| release-header | 27 |
| release-historic-tenant | 2 |
| release-historic | 4 |
| release-picker | 19 |
| release-presentation | 7 |
| release-ticket-links | 14 |
| releases | 22 |
| reserved-versions | 3 |
| usage-accounts | 1 |
| usage-dashboard | 10 |
| version-reveal | 4 |
| **Total** | **194** |

Unique unprotected risks also stay where no proven replacement exists: the two mounted Delivery section-switch cases, the new lane-growth/hover/fold coverage, retained failed Flow preferences, current-project writes/Undo, physical list/menu motion, release switch and public-binding refusals, stale/partial data and failed-save recovery. The runtime gaps include release-surfaces roadmap/frozen projections, usage-dashboard held-response rejection, accounts-overview incomplete inventory and aeon-635 pending/version loading. Both production cold-load request/lazy-module cases keep their original bounds, configuration and shared production-build identity.

## Current-round verification and timing

PASS: `node scripts/test-prune/check.mjs`, both cases in `node --test web/tests/test-prune-ledger.test.ts`, canonical manifest validation and `git diff --check`. TypeScript syntax passed for all 21 affected/merged test files. An AST comparison against the before commit confirms all 116 surviving browser test bodies in the 19 cut owners are byte-for-byte unchanged, except the webcore screenshot path. The ledger test includes missing frozen mapping, dangling destination and missing new-main mapping negative controls. Its P01 snapshot checks use the immutable baseline; no historical replay evidence is rewritten.

The before measurement uses commit `2f26947bb` on mbp2606 through LEAD's `remote-spec.sh`, with one worker and no local fallback. Initial attempts returned exit 3: `remote busy: 2/2 remote runs (CI pool 0/4)` and `remote busy (no free mbp2606 slot: global cap / CI pool / battery); keep coding and retry in a few minutes, or rely on PR CI`. The boundaries baseline subsequently passed: 77 passed, 9 skipped / 86 registrations in a rounded 4.5 minutes. The last process metric is for merge-reports, not the complete browser run, so its wall time is excluded. The current-round before/after timing and complete affected-set execution are still pending; no runner-time saving or browser pass is claimed here. Full static checks belong to hosted CI for this cut round.

## Earlier replacement proof

The first round added five browser replacements and used a separate scratch branch for missing-behavior controls. All five failed on scratch commit `9bc7d7532`, with original production behavior restored on the work branch:

| Missing behavior | Observed failure |
|---|---|
| Reduced-motion CSS | Expected zero animations, received two |
| Portal authoritative refresh after 409 | Expected current `p-aeon`, received stale `p-pharos` |
| Held Flow hint preserving the selected time | Expected `Viewing 20:18`, received `Live · now 20:26` |
| Delivery retained rows after 503 | Retained-results status disappeared into the empty/error slot |
| Overview right resize target | Expected end below 1305, received 1305 |

That round's five affected specs measured 66 passed, 2 failed / 68 cases in a rounded 1.8 minutes before, versus 25 passed / 25 in 49.2 seconds after, approximately 59 seconds less. Both baseline failures measured a deliberately hidden QA label at 960 px. This predates the current main merge and is not the cutA benchmark or a clean-green full-P11 saving.

`delivery-flow-modes.unit.test.ts` also adds a production `useDeliveryFlow` subscription-lifetime case with mounted components, an injected clock and counted reads/sources. Its runtime execution and negative control remain unproven, so both original `delivery-tab-switch` browser donors remain. The first-round status-motion runtime suite passed four cases in 158 ms after a permitted small fallback. These historical results do not stand in for this round's affected-set execution.

No product behavior, contract, migration, permissions, CI activation flags, required checks or visual authority changes. Retired Journey features remain closed. No push, deployment or model review gate is part of this worker round.
