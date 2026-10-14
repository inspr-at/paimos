# Impact CI: shadow selection

AEON-1333 adds rollout step 1 of Impact CI. Every PR publishes the
`impact-ci-shadow-plan` artifact and a job summary from `tier-plan`. The plan
records what browser selection would have done; execution still uses the
existing lane, tier decision and shard matrices. No runner or required check
consumes the shadow plan. This step does not yet reduce CI execution time.

The corrected baseline is **724 ESSENTIAL / 1,414 GATED-FULL / 2,648 NIGHTLY
browser registrations**. The larger 1,382 / 3,045 / 4,378 figures count all web
registrations, including unit tests. These are separate tier populations, not
cumulative suite sizes. The full gated browser baseline is 2,138 registrations.
The planner records the current counts in each plan.

The read-only GitHub API check on 2026-10-14 found `CI_IMPACT`,
`CI_AFFECTED_LANE`, `CI_TREE_REUSE` and `CI_MG_REUSE` unset at repository level.
Main ruleset 24240960 is active and requires `go`, `web`, `release-check`, `e2e`
and `migration-compat`. Its merge queue uses `ALLGREEN`, three concurrent
builds (`max_entries_to_build`) and up to five entries per merge
(`max_entries_to_merge`). No variables or rulesets were changed. The recorded
snapshot is `scripts/ci/impact-shadow-baseline.json`; it is an observation,
not a replacement for live branch protection.

Plan version 1 (`policy: impact-ci-s1`) contains immutable base/source-head and
checkout commit/tree identities, run/attempt, the actual execution decision,
`browser_scope: none | affected | gated-full`, selected browser identities,
12 deterministic shard assignments, reasons, fallback reasons and the exact
gated cases that would have been skipped. Browser identities include file,
project, configuration, title and repeated-title occurrence. First adoption or
unavailable trusted code publishes a full fallback with `complete: false` and
explicitly unavailable shard accounting; it never claims skipped coverage.

The shadow planner and its dependency graph come from the immutable PR base.
Candidate files are parsed as bounded data. Imports are supplemented by
navigation ownership of the application entry/router and URL-loaded harness
scripts. Computed imports own all possible local inputs conservatively. Source
selection additionally requires complete observations for the browser
catalogue. Unknown, deleted, renamed or unmapped files, missing ownership,
CSS/assets/data, shared backend/API/configuration inputs, incomplete observations
and excessive fan-out all select the full gated browser suite. Documentation
and pure timing changes may propose no browser shards. Promotions execute;
demotions, exclusions, case/launch ownership, project, flags, environment and
configuration changes remain full. These proposals have no execution effect
in S1. Go, migrations, release and real-server smoke retain their current CI
decisions; merge-group and nightly execution retain their full scopes.

Shadow map lookup uses an Actions read token and successful main-branch
nightlies. A `browser-deps-v1` fingerprint is recomputed from immutable Git
blob identities covering frontend source/routes, harnesses, fixtures, web
configuration, catalogue/launch policy, observation code, API inputs, release
manifest and nightly workflow. Unrelated commits can share this fingerprint;
changed dependencies cannot. All shards must have complete, passed case
observations matching the selected run/attempt, tree, fingerprint and catalogue.
Legacy evidence is accepted only for the exact tree. Lookup is bounded and
fails wide when evidence cannot be fetched or verified. The established
execution selector retains its exact-base-SHA lookup during S1.

Production performance tests map bundled JavaScript to every frontend module,
so bundling does not make an otherwise healthy nightly observation unusable.
Lost channels, workers, popups and missing cases still invalidate completeness.
A compatible successful nightly using the new observation code is needed
before fingerprint-based source narrowing can appear in shadow evidence.

Set the shared repository variable **`CI_IMPACT=off`** to roll back. The
classifier and tier publisher force full execution, tier runners reject narrow
decisions, nightly disables observation while continuing the full catalogue,
aggregates reject narrow lanes/layouts and reuse, and proof lookup and aggregate
reconfirmation reject evidence produced before rollback. Required check names
and explicit expected-skip checks remain in place; failures, cancellations,
unexpected skips and missing dependencies stay red. Unset or other values keep
S1 shadow reporting and the existing execution behavior.

Selector risks and switch behavior are covered in
`scripts/test-tiers/impact-plan.test.mjs`, imported by the existing unconditional
`go-static` selector regression lane. New files are covered by the existing S7
platform/CI ownership slice; no browser/Go case membership changes are needed.
