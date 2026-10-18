# Impact CI: shadow selection

AEON-1333 adds rollout step 1 of Impact CI. Every PR publishes the
`impact-ci-shadow-plan` artifact and a job summary from `ci-plan`. The plan
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

Since AEON-1337, the shared `ci-plan` job checks out PR history once for the
trusted classifier and tier/shadow steps. The shadow bootstrap retains its exact
base and depth-2 merge fetch, so it also remains valid with a shallow checkout:
`merge-base` needs both parent objects. See [planning and capacity](impact-ci-planning.md).

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
`scripts/test-tiers/impact-plan.test.mjs`, imported by the existing
`go-static` selector regression lane. New files are covered by the existing S7
platform/CI ownership slice; no browser/Go case membership changes are needed.

## Area qualification and execution (AEON-1336)

S4 is opt-in through the exact values `CI_IMPACT=on` and
`CI_AFFECTED_LANE=on`. Source changes then require qualification in
`scripts/ci/browser-area-rollout.json`. Every changed source must have exactly
one area owner. Shared backend inputs, unknown areas, missing observations,
unsupported inputs and uncertainty keep the full gate. The immutable base
planner uses the S1 import, route and harness ownership graph, and accepts
successful main-nightly observations with the trusted dependency fingerprint.

Qualification pins the historical replay corpus by SHA-256. It counts only
source-supported, bisected, ticket-confirmed, fix-diff-confirmed or recorded
browser regressions whose historical tree and trusted graph identities are
bound. Unresolved and inherited attribution rows are reported separately and
cannot qualify an area. Every accepted regression in an area must have been
selected by S1, and at least one must have used affected scope. Zero misses
from full fallbacks alone do not prove narrowing. A confirmed miss rejects
the area even if its configured mode remains `affected`; setting its mode to
`gated-full` is the per-area rollback. `CI_IMPACT=off` remains the immediate
global rollback, including execution, aggregates and reuse.

**No real area is activated by this change.** The checked-in corpus has 294
browser regressions with accepted causal evidence and 291 S1 misses. The first
candidate areas (`agents`, `releases`, `work`) all have misses, so they remain
`gated-full`. Expanding the rollout requires new historical shadow evidence,
an updated corpus pin and a deliberate area-mode change. Synthetic passing
fixtures test the activation path; they are not production qualification.

For a qualified area the tier planner emits an independent, bounded
`browser_scope=affected` execution plan containing its base and checkout SHAs,
area IDs, complete browser catalogue hash and selected native case identities.
It includes essentials, imports, route/harness owners, observed dependencies,
promotions and recorded drift obligations. The existing measured shard packer
sizes that selection. This execution plan comes from the trusted tier planner;
the `impact-ci-shadow-plan` artifact stays observational.

The complete plan travels as `affected-browser-plan`, containing
`browser-plan.json`; the ci-plan output contains only its SHA-256. Browser
runners download the file and verify that digest before parsing the bounded
contents. Case lists never enter an environment variable, whose per-string
limit on Linux is smaller than even the essential browser catalogue. A token
on the planner does not enable legacy browser narrowing: execution-map lookup
requires the opted-in, qualified S4 trusted path.

Browser runners verify the native catalogue and immutable identities before
using the plan. Candidate import-edge deletion cannot remove a planned case.
Missing, stale, malformed, duplicate or drifted plans widen to full execution;
explicit full/all requests and the kill switch take precedence. Measurement
reports label affected scope, and the existing full-tier proof rejects it for
merge-queue reuse. The `web` aggregate validates planned scope and still
requires browser success for affected runs. The five required check names,
merge-group full scope, nightly `--all` execution and real-server smoke are
unchanged.

Only affected PRs download the browser execution plan; full and queue runs
receive empty file and digest values. The aggregate validates scope before its
unchanged read-only proof sequence, including early reuse exits. Checkout, runtime setup
and proof reconfirmation must all succeed. The full-run workflow pin checks
the exact plan binding before comparing every existing job field.

The S4 behavior and rejection cases run in
`scripts/test-tiers/browser-areas.test.mjs`, imported by the existing fixed
`go-static` selector regression lane. New scripts use the existing S7 ownership
glob; no tier rows, application dependencies or web launch records are added.
