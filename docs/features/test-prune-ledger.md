# Test-prune risk and budget ledger

AEON-1673 implements P01 of AEON-1668 / GUI-37 under epic AEON-1670. It adds planning data and a lineage check. Existing tests, tiers, browser launch records, CI flags, required checks and historical replay evidence retain their behavior.

The person-approved retirement/demotion note is Markus's standing approval of the entire prune program on **2026-10-22 at 22:15 Europe/Vienna**, recorded by the coordinator in the AEON-1673 brief and attributed to **AEON-1670**. That approval covers the planned removals of redundant expectations. Every unique safety risk still needs its concrete replacement and proof before a cutting package removes the original. Tick's review of the visual states and immutable references is a separate prerequisite, currently pending.

`scripts/test-prune/baseline.json` freezes native registration identities, tiers, tags, post-audit catalogue counts, assembled per-spec launch policy, timing metadata, and SHA-256 pins for their inputs and the historical replay evidence. Its source is commit `78200ff4b01f9b93e001bc943953a67af9caa7a1`; this is a refresh of the committed registration catalogue, without browser execution or fresh native enumeration. The source audit used `52814e65e62a9888525f1fcec3aeb7617685b33b`.

The refreshed browser catalogue is **5,318 cases across 378 owners**, with **1,169 ESSENTIAL, 1,421 GATED-FULL and 2,728 NIGHTLY**. Existing full selection includes **3,092** browser cases. Compared with the audit's 5,316, `ui-audit-rules` and `start-agent` each gained one registration. There are also 963 Node and 3,272 Vitest registrations; the seven separately configured e2e registrations remain outside the proposed browser budget. Timing estimates remain those of the source audit, rather than newly measured savings.

`scripts/test-prune/risk-ledger.json` contains all 378 spec budgets, the exact logical core **C01–C29**, and 375 destination records. The target stays **338 = 256 functional + 2 cold request-bound + 80 visual** cases; core cases are included in those owner budgets. Native bindings of future consolidated core cases remain explicitly pending implementation.

The **647 protected lineage rows** cover all 455 historical drift cases (451 browser and four runtime), six Go drift obligations, all 31 mixed delete-candidate groups and all 155 currently tagged browser cases. The groups' 308 historically reported cases are metadata, not the current tagged population. All 25 unresolved drift titles retain their original text and current whole-file fallback identities. Each risk points to retained behavior, a named runtime suite, a visual state, or approved retirement with a surviving destination. The three new runtime recipients (`ui-audit-rules`, `native-program` and `native-diagnostics`) are recorded as planned; naming a recipient does not claim equivalence has been proven.

Run the lightweight check with:

```sh
node scripts/test-prune/check.mjs
```

The check derives required risks from the frozen source independently of the editable mappings. Missing rows, empty or unknown destinations, altered historical fallback identities, duplicate core/state identities and budget mismatches fail with exit 1. P01 additionally rejects removal, demotion or modification of existing catalogue rows and changes to pinned browser sources, launch/configuration/host/build/aggregate policy or historical evidence. The newly added ESSENTIAL Node test is an additive registration made through the canonical tier writer.

For later phases, an actual removed or demoted protected registration must have a verified replacement with evidence and current native identities. Any cut also requires Tick-approved references. Updating `phase` alone cannot permit an unverified cut. Current native collection, canonical manifests and current drift-obligation remapping remain the cutting package's responsibility; historical replay records must remain intact.

The behavior test is `web/tests/test-prune-ledger.test.ts`, also imported by the existing static tier-policy test entry point. Its negative-control fixture, `scripts/test-prune/testdata/missing-destination.json`, removes the destination and current registration of the protected late ticket lookup after a person/workspace switch. The check must report that exact missing destination. The test also rejects deletion of entire lineage rows, dangling destinations, premature cuts/demotions, policy drift and incomplete visual checkpoints.

`scripts/test-prune/visual-manifest.json` is ready for Tick's review: **18 independently owned states and 80 explicit checkpoints**, using 390×844 and 1440×900 in both themes, plus 1024×768 for Agents, Decision Desk, Account use matrix and Quote editor. It records the source audit's deliberate long/partial/conflict fixtures and requires actual image comparison, axe, readiness/font barriers, reduced motion and clipping/overlap/console checks. Interaction and ±0.5 px control-stability proof remain separate.

The proposed reference authority is an existing authorized **PPM / INSPR attachment on AEON-1670**, retained for the lifetime of the visual set. Tick approval, attachment identity, archive digest, checkpoint member digests and a successful availability verification are explicitly unset. P02 must supply and verify those values before cuts. CI captures stay in the existing artifact flow under `testInfo.outputPath()`; PNGs are never committed. An unavailable, expired or mismatched reference fails and requires a newly reviewed reference. The current run cannot become its own baseline, and automatic rewriting is forbidden. This package captures or publishes no baseline and introduces no credential.

New source ownership uses the existing `scripts/**` S7 and `web/tests/**` S9 slice rules; no additional slice pattern is necessary.
