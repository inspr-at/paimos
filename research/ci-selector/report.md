# AEON-1485: browser selector quality

Keep every browser area on `gated-full` and drop the current affected-only
rollout proposal. The measured ownership candidate reduces misses from **291
to 10 of 294**, but agents, releases and work all still miss cases. Its safe
version has zero misses only because **all 294 cases use the full browser
catalogue**. That is broader and more expensive than the existing gate; it is
not evidence for enabling an area. No workflow or area is activated here.

## Reproduction and provenance

This branch is based on unmerged `work/aeon-1336-ci-s4-affected`, commit
`da9f418e756a5da7483103d7136b22d597d367dc`, as permitted by the brief.
The decisions in AEON-1336's LEAD comment and AEON-1332/concept v2 are retained:
unknown impact stays broad, the merge queue stays full, and narrowing needs
zero misses on historical trees rather than a calendar wait.

The corpus is the unchanged `scripts/test-tiers/affected-replay.json`, SHA-256
`d960c3419d4522a04f90c4b61ffa12eb96fc09d3b44413826c44410e57c51d59`, matching
`scripts/ci/browser-area-rollout.json`.
[browser-candidate-measurement.json](../../scripts/test-tiers/browser-candidate-measurement.json)
contains all changed file lists, observed specs and exact failure identities,
per-case classifications, base/tested SHAs, selection results, fallback reasons,
graph summaries and implementation hashes.

```sh
node scripts/test-tiers/browser-candidate-replay.mjs --write
node scripts/test-tiers/browser-candidate-replay.mjs --check
node --test scripts/test-tiers/browser-candidate.test.mjs scripts/test-tiers/browser-areas.test.mjs
```

These commands read local Git objects and launch no browser. Missing history,
an altered corpus pin or a missing failure registration fails the replay;
there is no network fetch or substitution of today's graph. Source reads are
preflighted by Git blob size and bounded by file, total bytes, entries and
edges. `--check` compares the complete regenerated artifact, including source
hashes, rather than just its headline counts.

There are 294 supported browser observations, 248 distinct failure identities,
47 causing commits, 45 trusted causing-base trees and six tested trees.
The other 1,182 browser rows have unresolved or inherited attribution and are
excluded, following S4. Cases are observations, not independent bugs or PRs.
Area membership follows S4: a case belongs to each area prefix touched by its
causing diff. Area totals therefore overlap and must not be added.

The replay uses each causing **base** for dependency ownership and the tested
historical tree for its catalogue, as the S1 corpus does. It does not learn
edges from the fix or use the observed spec to build the mapping. The pinned
legacy `PR-REPLAY-1` row records its tested tree in `treeBeforeFix` but omits
`observed.testedSHA`; this exception remains explicit in the artifact. Nothing
is backfilled into the corpus to make it qualify.

## Why S1 missed 291 cases

Every one of the 291 misses was `NIGHTLY` in the tested historical catalogue.
Every missed S1 plan had `scope=gated-full`. S1's literal fallback selects
ESSENTIAL and GATED-FULL, so it discards the failing cases even when their
specs could be connected to a changed component. These are shadow replay
misses, not evidence that the currently shipped S2 gate lets them escape: S2
already carries explicit drift obligations, and its existing corpus test
checks that gate separately. This experiment does not count S2's curated
historical obligations as a newly discovered mapping.

The first non-documentation S1 fallback reason gives this exclusive partition:

| S1 fallback trigger | Missed observations |
| --- | ---: |
| Shared or unknown input, including backend/API/CI/rules changes | 239 |
| Catalogue membership changed | 30 |
| New, renamed, deleted or unmapped input | 12 |
| Runtime browser-impact map unavailable or stale | 5 |
| Fan-out above 300 additional browser cases | 5 |
| **Total** | **291** |

The mapping and the execution policy are separate problems. Merely making
runtime observations available would not fix the 281 misses whose recorded
first fallback was something else. Thirty-two misses even changed the
observed spec itself. Ninety-three failed case identities were not registered
at the causing base; in 92 of those observations the spec file itself was
absent from that base.

Changed-file categories below are **overlapping diff characteristics**, not
claims that each listed file independently caused a failure. Full merge diffs
often cross several categories; the code cannot infer a single causal file
from a commit-level attribution.

| Changed input category | Misses whose causing diff includes it |
| --- | ---: |
| Feature components | 282 |
| Shared libraries/composables under `web/src/lib/` | 282 |
| Spec edits, including specs other than the observed failure | 287 |
| Test fixtures, helpers or harnesses | 214 |
| Registry, CI, scripts or instruction files | 218 |
| Stores | 167 |
| Backend or API contract | 160 |
| Shared top-level components | 148 |
| Router, application entry or root HTML | 123 |
| CSS, including tokens | 32 |

Representative file-to-spec comparisons are retained per observation in the
artifact. For example, the release component change `cb663f5ac1` missed six
`aeon-635.spec.ts` cases even though the diff adds that spec. `f053c9b497`
changes TicketTable, TicketWorkspace, API/list libraries and ProjectView, but
the four observed `live-list.spec.ts` cases belong to a spec absent from its
base and not named in its diff. Store, token and router changes require
fan-out beyond a component's directory; browser harnesses can navigate to a
page without importing its component.

The inherited labels also limit interpretation: 260 missed observations are
STALE-SPEC/STALE SPEC/intended UI change, one is STALE SELECTOR, 23 are REAL or
PRODUCT REGRESSION, six are INFRA and one is FLAKE. This is a supported failure
corpus, not 294 independent product defects.

## Candidate and measured result

`scripts/test-tiers/browser-candidate.mjs` implements an offline experiment:

1. Reverse transitive imports and re-exports, with cycle handling.
2. Literal HTML/Vue script and style sources, CSS imports and inline Vite
   module URLs, including sources embedded inside a spec's HTML strings.
3. Navigation ownership of application entry/router and the local HTML
   harness set; a spec need not import the rendered component.
4. Explicitly changed specs own themselves even when they are new to the
   base. Their candidate import edges are not trusted.
5. ESSENTIAL cases remain selected. The diagnostic ownership selection
   includes mapped NIGHTLY specs rather than silently dropping their cases.
6. Shared stores, CSS/tokens, API/auth/session libraries and app/router entry
   changes fan out to the full catalogue. Mixed backend/configuration changes,
   unavailable/uncertain graphs, unknown files or owners, catalogue/base
   mismatch, non-PR events and excessive fan-out also use that fallback.

The module is not imported by a planner or runner. It requires both
`impact='on'` and `experiment=true`; its default and the existing kill switch
disable it. An offline diagnostic cannot alter Actions outputs, execution
proofs, reuse or required checks. The live rollout file is unchanged.

| Area | Supported cases | S1 misses | Ownership misses | Safe candidate misses | Narrowed cases | Qualified |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| agents | 131 | 131 | 4 | 0 | 0 | No |
| releases | 118 | 118 | 4 | 0 | 0 | No |
| work | 203 | 202 | 8 | 0 | 0 | No |
| All distinct observations | 294 | 291 | 10 | 0 | 0 | No |

“Ownership” includes ESSENTIAL cases, the import/route/harness closure and
explicit spec changes, before safety fallbacks. Adding the original S1
selection to this set leaves the same ten misses. The initial import closure
alone left 25 misses; selecting explicitly added specs recovers 15 more.

The remaining ten observations form four groups:

| Spec | Case IDs | Causing commit | Count |
| --- | --- | --- | ---: |
| `live-list.spec.ts` | NR-1165–NR-1168 | `f053c9b497` | 4 |
| `role-picker-layout.spec.ts` | NR-1205–NR-1206 | `d15114c1a9` | 2 |
| `aeon-908-polish.spec.ts` | NR-1288–NR-1289 | `a3ca8c475a` | 2 |
| `decision-text.spec.ts` | NR-1290–NR-1291 | `c129580d1c` | 2 |

All four specs are absent from their trusted base and are not explicit changed
specs in their causing diff. Six observations are labelled stale specs and
four real regressions. Assigning future specs to old components using their
later contents would leak future ownership into the replay. The candidate
therefore reports those misses and widens instead of inventing edges.

All 45 historical source snapshots were read completely, but each contains
unsupported/computed or unresolved local references (including asset imports).
This deliberately small parser treats that uncertainty conservatively. It
does not claim that all such imports are actual bugs. The artifact includes
example files and counts so a later parser improvement can be measured.
Independent fallback barriers also matter: 270 observations mix a shared,
CSS/data or unknown diff input, 239 touch an explicit global fan-out input,
and 286 have catalogue specs absent from the trusted base graph. These counts
overlap. Removing one barrier is not sufficient evidence for narrowing.

The safe result is consequently full catalogue in 294/294 observations,
selecting 4,007–4,897 browser registrations per replay, compared with S1's
622–2,210. The diagnostic ownership set alone has a median 282 spec files.
These are case/spec counts, not runner-time measurements. No browser or
wall-time benchmark was needed or run. The candidate offers **no demonstrated
runner savings** on this corpus.

## Recommendation and validation

Do not qualify or activate agents, releases or work. Keep today's full gate,
S2 drift protection, full merge-queue/nightly coverage and the kill switch.
Drop affected-only browser execution from the current rollout plan and
optimise elsewhere (the measured no-browser lane, scheduling, flakes and
runner startup) under their own existing rules. The ownership experiment can
remain available for offline research. Revisiting it needs a representative
historical catalogue at the actual change, independently justified ownership
for harnesses/new specs, zero ownership misses and measured savings; a zero
obtained by running the entire catalogue is not qualification.

Tests cover transitive/cyclic imports, non-importing route and HTML harness
owners, fixture ownership, changed/new specs, unrelated specs, global fan-out,
unknown inputs, graph/size/edge limits, computed ownership outside the selected
closure, the 300-case budget, kill switch, missing history and corpus pins.
They are registered through the existing fixed static entry point
`scripts/test-tiers/test-tiers.test.mjs`; these scripts are outside the native
Go/web tier catalogue, so no synthetic tier or shard rows are added. New
sources already belong to S7 through `scripts/**`; the ownership registry
needs no edits.

Both targeted negative controls fail as intended: removing transitive
ownership fails the mapping test; changing full fallback back to the gated
subset fails the NIGHTLY-retention test. Original sources are restored and
the tests pass.

### Handover checks

- `node --test scripts/test-tiers/browser-candidate.test.mjs
  scripts/test-tiers/browser-areas.test.mjs`: 11 passed.
- `node scripts/test-tiers/browser-candidate-replay.mjs --check`: passed on
  the unchanged pin, checking all 294 proof rows and implementation hashes.
- `node scripts/test-tiers/cli.mjs manifests --check`: passed.
- `node --test web/scripts/ci-web-shard.test.mjs`: 33 passed after installing
  the previously absent web dependencies once, with browser download disabled.
  The earlier attempt failed on missing dependencies, not a selector assertion.
- `python3 -m unittest discover -s scripts/audit`: 33 passed after keeping the
  JSON artifact under the existing S7-owned `scripts/**` prefix. No registry
  ownership exceptions or added rows were needed.
- `/Users/markus/.aeon/lead/aeon/ci-static-locked.sh`: **exit 3 on both
  attempts**: “remote static check paused at night (night protocol: mbp2606 is
  shared with the CI pool); rely on the PR's hosted CI static job”. Full
  `node scripts/ci-static.mjs --merge-main`, including the complete selector
  suite, remains unproven. No heavy local fallback was run. Worker status
  remains below 100 until that required check runs on an approved host or CI.
- No browser run, model review, push to origin, deployment, workflow edit or
  area activation is part of this change.
