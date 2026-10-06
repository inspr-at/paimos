# Release

Aeon uses INSPR Calendar Versioning, currently INSPR-CalVer3 (`inspr-calver-3`). The coordinate is `YYMMDDhhmmss.0.0`: two-digit year, month, day, hour, minute, and second in UTC, then `.0.0`. It is SemVer-shaped and fixed width. `version.json` is the only source of that coordinate. The fields that matter for a release are `version_scheme`, `version`, `release_channel`, and `release_sequence`.

### From CalVer2 to CalVer3

Releases up to `260929113854.0.0` (release 10, sequence 105) were reserved under `inspr-calendar-v2` (INSPR-CalVer2). Both schemes share one coordinate, so versions keep sorting as time and every earlier tag stays exactly as published. From release 11 on, a reservation writes `"version_scheme": "inspr-calver-3"` in `version.json` with a coordinate later than `260929113854.0.0`; `scripts/verify-release.mjs` rejects a new reservation that still declares `inspr-calendar-v2` (`LAST_CALVER2` in that script, `releasehistory.LastCalVer2` in Go). Only the reservation changes; `release_sequence` continues.

The version pill uses the shared INSPR renderer: six segments (`YY·MM·DD hh:mm`, seconds on hover, focus or tap), never the `v` or `.0.0`. Copying always yields the exact canonical version, `.0.0` included. The label table (`schemes.json`) names the schemes INSPR-CalVer3, INSPR-CalVer2 and INSPR-CalVer1.

The git tag is `v` plus the `version` field, for example `v260926064658.0.0`. The coordinator creates it with `node scripts/create-release-tag.mjs --write` after the rehearsal and release approval below; the helper writes an annotated tag with a `Release <tag>` message. `scripts/release-tag.mjs` checks that a pushed tag has that shape; the release workflow rejects it unless `git cat-file -t "refs/tags/$tag"` returns `tag`. `scripts/verify-release.mjs` checks that `version.json` matches the scheme and that the vendored calendar presentation bundle under `web/src/vendor/calendar-version-display` matches `scripts/calendar-version-bundle-pin.json`. `just release-check` runs the verifier. A production web build runs the same check before it emits assets.

Development builds leave the linker version at `dev`. A release build sets:

```
-X github.com/inspr-at/paimos/internal/version.Version=<version>
```

with `-trimpath`. The server image, `aeon-cli`, and Linux `paimos-agentd` use `CGO_ENABLED=0` (`scripts/build-image-inputs.mjs` for the image). Darwin `paimos-agentd` is built on macOS with `CGO_ENABLED=1` and links LocalAuthentication. `scripts/build-release-binaries.sh` builds the distributed client binaries in `.github/workflows/release.yml`.

## Workflow

### Nix Go dependency hash (AEON-703)

The Linux `release-check-run` job builds `aeon.goModules` to verify the shared
`vendorHash` used by both flake packages. `ci-plan` selects it for PR changes to
`go.mod`, `go.sum`, `flake.nix`, `flake.lock` or the guard's wiring. Main, merge
queue and manual runs select it too; missing classification selects it safely.
The required `release-check` aggregate includes failures from this step.
Nix inputs are cached by OS, architecture and those four files, with cache
writes confined to main pushes. The dependency output is always rebuilt, so
an existing fixed-output store path cannot hide a stale hash; no application
compilation or Postgres is needed for this check.

Run `bash scripts/check-nix-vendor-hash.sh` from the repository root. After a
dependency update, obtain the new hash from a real `nix build .#aeon -L` hash
mismatch, update `flake.nix`, then rerun the check and package build. Do not
guess a hash or add a downstream override. These are package builds, never
NixOS configuration builds on macOS.

### Mandatory pre-tag rehearsal (AEON-531)

Every release PR must pass **`release-rehearsal`** in
`release-image-check.yml`. The coordinator treats it as a release-readiness
requirement; this change does not alter repository rulesets. PR runs exercise
the proposed code, but cannot authorize a tag. After merge, the workflow runs
on the release commit on `main`; if the automatic path-filtered run did not
start, dispatch it on `main` while that exact commit is the branch head.
Do not tag until the main run completes successfully:

```sh
gh workflow run release-image-check.yml --ref main
# After the run completes, from the exact main commit intended for release:
node scripts/create-release-tag.mjs
# After the coordinator's release approval, explicitly create the local tag:
node scripts/create-release-tag.mjs --write
# Push the annotated tag only through the coordinator's existing release procedure.
```

The check reads the exact workflow identity, paginated runs, every job and step,
and the unexpired receipt artifact for that SHA and run attempt. PR/fork/other
branch runs, a different commit, a newer failed or pending attempt, a skipped
platform or missing receipt all leave the gate closed. The tag workflow checks
the same receipt before either native signing work or image publication can
start. A merge/squash commit needs its own main rehearsal; an earlier PR receipt
never carries over. Receipts expire after 14 days; rerun on the same main commit
if needed. No tag, release, image or historic version is rewritten.
The tag helper requires a clean tracked checkout on `main`, validates the version,
presentation and frozen notes, rejects existing tags and rechecks the checkout
after the receipt lookup. Its default is a preview; explicit `--write` creates
only an annotated local tag at the checked immutable SHA, without pushing.

The rehearsal validates `version.json`, the complete pinned presentation bundle
and the release's own frozen notes; builds and smokes both native Linux images
from the production Docker context and read-only caches; exports BuildKit
provenance locally; builds both native Darwin agents with LocalAuthentication;
executes the exact production asset assembly and verifies all eight checksums.
It runs the production pin-bot in dry-run mode with fixture source/tag/attestation
observations and a local pin snapshot. The index and draft shell bodies are read
directly from `release.yml`: strict offline adapters exercise the index contract,
and the installed `gh` parses the actual attestation and nine-asset draft argv
against a closed loopback proxy, with an empty config and synthetic credential.
This catches incompatible flags and missing assets without calling the forge or
registry. Tap fixtures run without a live App. The receipt records the commit,
version metadata, release-workflow digest and disabled operations.

Rehearsal never signs, notarizes, pushes an image/cache, writes an attestation,
creates a tag/release/pin/tap PR, publishes or deploys. Its native checks verify
the unsigned daemon's embedded version and unsigned-image refusal. Real Apple
credentials/notarization, live registry provenance, paired Keychain ACL and
Touch ID qualification remain the existing post-tag gates; fixture success is
not evidence for those controls.

`scripts/release-rehearsal-coverage.json` binds every production release step to
its rehearsal action, executable counterpart, regression fixture or explicitly
disabled privileged operation. The workflow tests reject new jobs, new steps,
changed step bodies/flags and missing counterparts. When a release step changes,
implement its counterpart first, then review the updated fingerprint and run the
negative drift fixtures. Do not regenerate fingerprints as a substitute for a
counterpart. `docker-web-check.yml` retains its established check identity and
runs `scripts/build-image-inputs.mjs web`, the production host web build, on PR
changes to `web/**`, `internal/**/*.json` and its build inputs. Shared JSON
imports keep their full checkout paths; the obsolete Docker `web` stage no
longer compiles anything. Both image rehearsals also check these imports.

### Cross-family verdict and merge queue (AEON-411)

The required **`gate/cross-family`** is a commit status posted **outside
GitHub Actions** by `inspr-mbp2606-runner` (App ID `5134402`, existing
installation `166478088`). The companion ruleset in
`.github/cross-family-ruleset.json` binds that exact context to the App. Neither
a successful nor a skipped Actions job with the same name can satisfy the
binding. `.github/workflows/cross-family-preview.yml` is only a diagnostic
mirror named `gate/policy-preview`; it runs unconditionally on `pull_request`
and `merge_group: checks_requested`, with a read-only token. Pushes and manual
dispatches emit neither gate context.

The coordinator records the independent review's explicit `ok` and complete
reviewed SHA in the ticket, then posts `gate/verdict` using its approved identity:

```sh
# Set these from the completed review, never from an assumed verdict.
gh api "repos/inspr-at/paimos/statuses/$REVIEWED_SHA" \
  -f state=success -f context=gate/verdict \
  -f description="model=$REVIEW_MODEL; route=$REVIEW_ROUTE; review=$REVIEW_FILE"
```

Keep the description within GitHub's 140-character limit. The latest trusted
`gate/verdict` governs, including pending/error/failure revocation. The allowlist
in `.github/gate-posters.json` requires both login and immutable numeric ID
(initially `markus-barta`, ID `276789`). Other creators cannot grant or revoke
approval. The external App verifies those verdicts; it never conducts a review.

**Trusted poster:** install the bootstrap `scripts/trusted-cross-family-poster.mjs`
and its sibling `post-cross-family-gate.mjs` / `cross-family-gate.mjs` from one
independently reviewed main revision in the coordinator's protected tooling.
Its dedicated bare mirror must have origin
`https://github.com/inspr-at/paimos.git`. In write mode it first posts pending
using that installed trusted bundle, before a fetch or extraction can fail. On each invocation it refreshes
`refs/remotes/origin/main`, extracts the checker, poster and allowlist from that
commit into a private snapshot, and executes only those files. Candidate
commits are fetched as Git objects for parent/tree comparisons; no candidate
workflow, module, package install, hook or build runs. A PR checkout cannot
serve as the mirror. Missing main policy, failed fetches and invalid events fail
closed. The bootstrap itself is trusted installed code, never a PR-supplied
entrypoint. CODEOWNERS covers the workflows, checker, poster, bootstrap,
allowlist, ruleset payload and CODEOWNERS itself; the companion rule enables
code owner review for those paths.

The coordinator supplies an installation token for App `5134402` as the
process-only `GATE_APP_TOKEN`, never as a repository/Actions secret. The App
currently lacks the additional permissions this requires: the lead must grant
`statuses: write`, `contents: read`, and `pull_requests: read` and accept them for
this repository. Retain its existing runner permissions; this change does not
alter the runner controller or provision credentials. Authenticate event input
through verified GitHub webhook signatures or the coordinator's authenticated
API reads; arbitrary uploaded event files are not trusted dispatch requests.

```sh
# BARE_MIRROR and EVENT_FILE are absolute paths in the coordinator's own tooling.
# Default: read-only evaluation, with no status writes.
node /trusted/tools/trusted-cross-family-poster.mjs "$BARE_MIRROR" \
  pull_request "$EVENT_FILE"
# Explicit opt-in, used only by the coordinator's external watcher:
node /trusted/tools/trusted-cross-family-poster.mjs "$BARE_MIRROR" \
  pull_request "$EVENT_FILE" --write
# Queue checks use the signed checks_requested payload and exact synthetic SHA:
node /trusted/tools/trusted-cross-family-poster.mjs "$BARE_MIRROR" \
  merge_group "$EVENT_FILE" --write
```

The external watcher handles PR opened/synchronize/reopened and merge-group
checks_requested events; Actions is not the dispatcher. After posting or
revoking `gate/verdict`, the watcher re-evaluates the affected open PR and **all
active merge groups containing it** (and serializes evaluations per target SHA).
A missing watcher leaves the required status pending rather than approving.
Every write-mode invocation posts pending first, then success only after all
checks pass; errors post failure or leave pending if the API itself is down.
The PR status targets the actual PR **head SHA**, including a fork's head in the
base repository; the queue status targets **merge_group.head_sha**. Never copy a
PR's success to a group without verifying every constituent PR. The API writer
rejects a human or Actions creator; the ruleset's integration binding is the
server-side enforcement, including against a writer forging the context.

An exact reviewed head passes. Follow-ups pass only along the first-parent
chain when every step has two parents, the second parent is on main's
first-parent history, and `git merge-tree --write-tree` reproduces the actual
commit tree. Blobs, paths and modes must match. New edits, conflict resolutions,
normal commits (even empty or reverted), rebases and squashes require a fresh
verdict. Missing evidence, API errors, partial pagination and changed heads fail
closed. Queue checking reproduces every synthetic merge and resolves every
second-parent SHA against open main PRs. Multiple PRs, including forks, with the
same immutable SHA share one verdict check, but **every** matching PR is
revalidated as open with that SHA, base repository and main branch. Missing
matches and changed synthetic trees fail closed.

**Architecture decision:** choose external App statuses on the current Free
plan. [Required workflows](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#require-workflows-to-pass-before-merging)
would provide a trusted source workflow selected by repository/path and
`refs/heads/main` in an organization/enterprise ruleset, with `merge_group`
support. The org reports `plan=free`, and reading its rulesets returns HTTP 403
with an upgrade requirement, so this is not currently available.
[`pull_request_target`](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request_target)
uses base workflow code and could safely perform read-only PR API inspection
without checking out PR code. It does not establish trusted code for ordinary
merge-group workflows, and an Actions-name requirement remains spoofable.
[App-bound statuses](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#require-status-checks-to-pass-before-merging)
are supported for non-Actions Apps with status-write permission; the
[REST ruleset schema](https://docs.github.com/en/rest/repos/rules#create-a-repository-ruleset)
provides `integration_id`. The selected existing App avoids an invented ID or a
paid-plan assumption. Its token must remain confined to the trusted external
coordinator.

**Lead rollout, after bootstrap:** keep ruleset `24240960` and its required
`go`, `web`, `release-check`, `e2e`, `migration-compat` checks and merge queue
unchanged. Install/wire the external poster, grant/accept the App permissions,
then create the additive repo ruleset using the **exact JSON** in
`.github/cross-family-ruleset.json`. Do not require the diagnostic Actions check
or bind the required context to GitHub Actions (`15368`). Preserve a backup and
rollback by disabling only the new ruleset. The existing admin PR-only bypass
is retained as a deliberate operator control; the worker never uses it. A code
owner cannot approve their own PR, so use an independent owner review or the
operator's explicitly approved admin procedure when the PR author is the owner.
Coordinate the new ruleset/baseline with the owning nixcfg coordinator before
runner admission is enabled; workers make no foreign repo changes.

Before activation, the lead records live evidence: unreviewed PR denied; trusted
exact head and automatic main merge pass; workflow `if: false`/`exit 0` and a
forged Actions/user status cannot satisfy the App-bound rule; both heads in a
two-PR queue require review; duplicate-SHA fork PRs do not stall the queue;
revocation refreshes PR and group statuses. Local fixtures validate repo code,
not live GitHub enforcement. This worker changes no permissions, settings,
rulesets, watcher deployment, merges or queues. Acceptance remains with the
lead; the process runbook is PPM AEON `runbook/flywheel`, §2.5–§2.7.

### Test runner routing (AEON-438, AEON-459)

CI's hosted `runner-route` job calls `test-runner-route.yml`, requests four idle
slots, and selects the entire Go batch behind independent event, ref and
rerun-attempt guards. The manual smoke workflow calls its own router for one
slot. Only `push` and `workflow_dispatch` on `refs/heads/main` may use the pool:

- A verified main push: `runs-on: [self-hosted, Linux, ARM64, mbp2606, mbp2606-push]`.
- A verified main dispatch: `runs-on: [self-hosted, Linux, ARM64, mbp2606, mbp2606-dispatch]`.

The controller mints the base labels `self-hosted, Linux, ARM64, mbp2606` plus
**exactly one** class label matching the verified run's event; the configured
sets contain neither both classes on one runner nor hosted-looking labels
([default.nix:115–143](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L115-L143), [aeon_builder.py:120–123](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L120-L123), [aeon_builder.py:857–875](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L857-L875)).
`could_take` is a case-insensitive **subset** check against the complete minted
label set, so a competing job need not request a class label to match
([aeon_builder.py:93–100](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L93-L100)).
The reviewed workflows route PRs to `ubuntu-latest`;
a PR can modify those workflows or the guard, so runner-side admission is the
enforcement boundary. The manual `Test runner smoke` workflow exercises the same
router and small Go/Node checks. Go tests use four pool shards when routing
admits the batch, otherwise the existing seven hosted shards. Timing budgets,
static checks and the required `go` aggregate always run hosted.

**Web CI (OPS-257):** hosted `web-setup` runs typecheck and lint, builds the
current web, and shares its dependencies and build through the run's
`web-runtime` artifact. Four file-preserving `web-unit` shards start beside
the browser shards from that runtime; shell preparation and unit checks no longer
delay browser fan-out. Unit shard 1 also runs shard-policy regressions and
browser-runner safety once. Spec-only PRs retain one unit job with the existing
unit command; docs-only PRs skip all three web job families. Dependency and Chromium
caches are keyed by the lockfile hash and resolved Playwright version; each of
12 hosted `web-shard` jobs restores that runtime and installs only Chromium's
system libraries. The shard command is `npm --prefix web run ci:web:shard --
i/12` for exact-spec inspection; full-lane execution uses
`scripts/test-tiers/cli.mjs run web --shard i/12` with zero retries. Traces and
screenshots are retained in `web-shard-i-evidence` for seven days. The required
check remains exactly `web`: its unconditional aggregate rejects failed,
cancelled or skipped setup, unit shards or browser shards in the full lane.
All four unit measurements and twelve browser measurements feed the tier report;
nightly retains its existing once-only `web-unit` evidence. Skipped Actions jobs
with zero timestamps report absent duration; executed jobs with invalid timestamps
still fail accounting. Spec-only reports explicitly use `untiered` coverage and
claim no tier case passes; its native checks still block `web`.

**Affected PR planning (OPS-257 / AEON-743):** `CI_AFFECTED_LANE` is a repository
variable, default off. Only its exact value `on` enables new rules; unset, `off`
and every other value preserve old PR selection byte for byte (the selector test
replays all 80 recorded merged-PR file lists plus one list per rule against the
frozen phase-1 selector). The workflow environment passes it to the planners and
tier runners. Merge groups always use the full gated catalogue and the full
layout regardless of the switch. Main push, manual and nightly selection keep
their established behavior. Required check names, skipped-job accounting, tier
evidence, tree reuse and unconditional `migration-compat` stay unchanged.

**The bar (OPS decision, phase 2):** the PR lane is feedback only. The merge
queue is the authoritative full gate, main push stays full or exact-tree reuse,
nightly stays full, and any change to CI machinery stays full in the PR lane
too (`.github/**`, `scripts/ci-*`, `scripts/ci/static-checks.json`,
`scripts/test-tiers/**`, `scripts/test-tier-go/**`, `scripts/releaseworkflow/**`,
`go.mod`/`go.sum`/`go.work`, `web/package*.json`, Playwright, Vite and
TypeScript configuration, `web/scripts/` except the three manifest regression
tests). A PR can already edit its own `ci.yml`, so narrowing the PR lane creates
no new bypass while the merge group is full; the phase-1 "trusted bootstrap"
concern is therefore not a blocker for narrowing. The tier planner still runs
the base-SHA snapshot of its classifier, graph and manifests, which keeps lane
decisions reproducible, not as a security boundary. A narrowing miss costs one
merge-group ejection (about 12 to 25 minutes), so rules narrow wherever a
plausible, cheap-to-compute mapping exists and stay full where a miss is likely
or the mapping is unknowable.

**Modes and layouts.** The planner publishes `mode` (`full` or `essential`) and
`layout` (`full` or `static`). `essential`/`full` is the phase-1 shape: two Go
shards, two browser shards, four unit shards, timing, static, release, smoke and
migration jobs (22 hosted jobs). `essential`/`static` keeps only `ci-plan`,
`tier-plan`, `runner-route`, `go-static`, `go`, `web-setup`, four `web-unit`
shards, `web`, `release-check-run`, `release-check`, `e2e`, `migration-compat`
and `tier-measurements` (16 jobs): no Go shards, no timing, no browsers, no
server smoke. The `go`, `web` and `e2e` aggregates accept exactly those skips,
only for `pull_request` events with lane `full`, and reject any other layout
value. The static layout applies only when every changed path is docs-like,
a manifest (R1), audit tooling (R5) or an always-on regression test (R9), and
no promoted case needs a Go or browser shard. `tier-measurements` treats the
skipped shard jobs as expected and still requires unit evidence.

Rules, each with how a regression could slip through the PR lane and what
catches it:

- **R1 manifests** (`scripts/ci/*.json` tier, flaky and baseline registries,
  `scripts/ci/go-shards*.txt` weights, `web/ci-web-shards.json`,
  `scripts/migration-policy*.json`, `scripts/audit/*.json`, and the manifest
  regression tests `web/scripts/{ci-web-shard,aeon-676-ci,aeon-681-ci}.test.mjs`):
  static layout. `go-static` re-validates the Go manifest against native
  collection (`check go` now also rejects malformed timing weights), `web-unit`
  shard 1 runs the shard manifest tests and `ci:web:shard:check`, the tier
  runner validates the web manifest, `migration-compat` applies the policy
  JSON and `release-check-run` runs the audit suite. Tier manifests are diffed
  against the event base: a row promoted to GATED-FULL or ESSENTIAL (absent or
  lower tier at base) executes; unit promotions stay static, Go or browser
  promotions switch to the full layout; new NIGHTLY rows need only the checks.
  A missing base manifest fails closed to full. Slip: a shard regrouping that
  starves a browser group only shows when browsers run; caught by the merge
  group's 12-shard full run.
- **R2 API contracts** (`api/**`): essential/full layout with the Go packages
  whose sources name the changed file (grep over `internal/`, `cmd/` and
  `scripts/` Go sources), their bounded reverse dependants, web typecheck and
  lint in `web-setup`, the smoke in `e2e-run`, and ESSENTIAL web. This tree has
  handwritten web wire types and no generated API code, so there are no
  generated-type importers to select. A diff that also touches the OpenAPI lint
  package `internal/reportercontract/`, codegen configuration or generated
  sources stays full; a contract nobody reads stays full. Slip: a response
  shape change that only a NIGHTLY browser spec notices; caught by nightly,
  while the Go contract tests and the smoke run in the lane.
- **R3 web helpers and fixtures:** unchanged from phase 1: ESSENTIAL plus every
  transitive importing spec and unit; unknown, deleted or unimported helpers
  and helpers imported by more than 15 specs stay full; the 300-browser-case
  layout bound applies. Harness pages loaded by URL are not import edges (an
  experiment with page edges cost more PRs to the 300-case bound than it won).
- **R4 migrations** (`internal/db/migrations/*.sql`): essential/full layout.
  A SQL-aware tokenizer handles strings, quoted identifiers, dollar quotes,
  and nested block/line comments together. Only completely parsed statements
  may narrow: simple CREATE TABLE, ALTER TABLE ADD COLUMN, CREATE INDEX, and
  INSERT/UPDATE/DELETE on a plain named table with constant values and simple
  predicates. Quoted names and the `public` schema are supported. Every token
  must belong to the whitelist. Any unsupported statement or tail, DO/function
  body, CTE, TRUNCATE, complex expression/constraint/policy, ambiguous escape,
  malformed quote/comment or empty intermediate statement makes the **whole
  migration full**, even if preceding statements have known objects.
  Every Go package whose sources mention one of those identifiers runs, plus
  `internal/db` itself (the owning package: migration runner, RLS bootstrap and
  split-parity tests), bounded reverse dependants, `migration-compat` as always
  and the real-server smoke. Browser specs are API-mocked and never reach the
  database, so the web side stays at ESSENTIAL. Unreadable migrations, no
  recognised object, no referencing package, or more than the consumer bound
  stay full. Other `internal/db/` sources and `internal/dbtest/` stay full. The
  migrations README belongs to `internal/db`. Slip: a package that reads a
  changed table only through a view or function defined elsewhere, or through
  dynamically assembled SQL; caught by the merge group's full Go run and by
  `migration-compat` for the previous release.
- **R5 audit tooling** (`scripts/audit/**`): static layout; `release-check-run`
  runs the audit unit tests and coverage check in every lane. Slip: none in
  the product; a broken audit script fails its own job.
- **R6 release data** (`version.json`, with `internal/releasehistory/data/`
  through the ordinary package mapping): packages and web tests that name
  `version.json` (`internal/releasehistory`, `internal/releases`,
  `tests/calver3.test.ts`) plus the release checks in `release-check-run`.
  Slip: a build-time consumer that reads the file under another name; caught
  by the merge group and the release rehearsal.
- **R7 Go test data** (`internal/**/testdata/`, `cmd/**/testdata/`): the owning
  package through the ordinary mapping plus every Go package and web test that
  names the file. Slip: a sibling package reading the directory with a
  computed path; caught by the merge group.
- **R8 harness pages** (`web/tests/*.html`): the specs that navigate to the
  page by name. Slip: a page reached through a variable URL; caught by the
  merge group.
- **R9 always-on regression tests** (`scripts/check-migrations.test.mjs`,
  `scripts/migration_compat_probe_test.py`): static layout; `migration-compat`
  executes them in every lane. Their executables (`check-migrations.mjs`,
  `migration-compat.sh`, `migration-compat-probe.py`) stay full.
- **Docs-like files** (`docs/`, root notices, any Markdown outside `testdata`,
  `internal/` and `cmd/`): no tier impact; Markdown inside Go packages maps to
  the package. Every other path keeps its phase-1 handling: ordinary Go and
  web sources select the changed area, everything unmapped stays full.

Bounds: R3 keeps 15 specs; mapped browser fan-outs above 300 cases keep the
full layout; optional Go reverse dependants above 300 extra cases are dropped;
consumer rules (R2, R4, R6, R7) stay full above 1000 extra gated Go cases,
because the two essential Go shards carry about one full hosted shard each and a
wider set is cheaper on the seven-shard layout. `consumerCaseBound` in
`scripts/test-tiers/core.mjs` is the knob; hosted timings calibrate it.

With `CI_AFFECTED_LANE=on`, `tier-plan.outputs.lane` is the single effective
workflow lane. Wider trusted coverage wins: `full`, `essential` and `static`
planner modes force the `full` aggregate lane, with the trusted layout deciding
which jobs execute. `spec-only` is retained only when both classifiers say
`spec-only`; `docs-only` only when the trusted planner says `docs`. A raw `full`
lane never narrows. Missing or invalid planner data fails closed to full; merge
queue, main and manual events stay full. With the flag off (including unset or
any value other than exact `on`), the raw `ci-plan` lane is preserved unchanged.
Every execution condition, shard matrix, runner tier selection, full-execution
check, required aggregate and tier measurement uses this effective output.
Only `tier-plan` reads the raw classifier lane. All consumers depend on successful
tier planning; a new spec missing from the trusted graph therefore runs full
validation and inventory checks, even if the raw classifier called it spec-only.

The runner honours `AEON_TEST_TIER_MODE` from the trusted planner (full,
essential, static or spec-only); a candidate graph cannot narrow a supplied
full decision. Candidate uncertainty or an explicit planner/candidate layout
disagreement widens to full. `AEON_TEST_TIER_LAYOUT`, when supplied, preserves
the planner's layout. Explicit `--full`/`--all` retain their existing meanings.

Consumer scans preflight the complete file set before reading: reaching 20,000
files, 100,000 walked entries, 64 MiB total or 2 MiB per file invalidates the
whole scan. Source and migration reads are bounded, reject binary/invalid UTF-8
and non-regular files, check file identity/size around reads, and reject symlink
escapes. Scan errors and symlinks discard partial results and force full for
the whole affected selection. These checks do not alter the flag-off selector.

Replay the 80 recorded merged PR file lists without browsers using
`node scripts/test-tiers/replay.mjs` (`--json` adds a summary and exact per-PR
counts and reasons). The fixture pins the old selector and Go graph to
`b6f74faa11d506efd3d21387ca4e2f5a9c687dcf`; replay uses the current manifest
catalogue, web graph, Go sources and migrations rather than each historical PR
tree, and cannot reconstruct historical manifest promotions (R1 edits replay as
registration-only). Result after OPS-257 L4 fix round 2: old lane 16 of 80
essential (20%); new lane 36 of 80 narrowed (45%: 20 newly essential,
16 unchanged, 0 static
because every sampled manifest or audit PR also changed sources). Projected
hosted jobs for the sample fall from 2710 to 2420 (full 37, essential 22,
static 16, spec-only 12, docs-only 9). The pre-fix lane narrowed 38; two of
those PRs now correctly stay full on unsupported SQL. PR 235 retains its
essential tier selection but widens its raw spec-only workflow lane to full
(22 jobs rather than 12), following trusted planner precedence. The 44 PRs that stay
full: CI machinery
18 (mostly `ci.yml`), R3 helpers above 15 specs 9 (`work-fixtures.ts`,
`agents-fixtures.ts`, `capacity-fixtures.ts`), unnarrowed `internal/db/`
sources or ad-hoc scripts 8, unsupported SQL migrations 6, one browser fan-out above
300, one deleted-file replay artifact (PR 224 narrows on its live tree) and one
`.dockerignore`. The 50% target is missed by four PRs on this sample; SQL
uncertainty is a safety fallback, not a reason to loosen the whitelist.

Rollout stays coordinator-owned: keep the variable unset while this change is
reviewed and merged, measure the next full runs (rule reasons, selected cases,
promotion counts, jobs, PR wall time, merge-group and nightly failures), then
set `CI_AFFECTED_LANE=on`. Acceptance for keeping it on: at least 50% of PR runs
essential or static with p50 wall time at or below 5 minutes, merge-group red
rate at or below 10%, and nightly finding nothing the lane missed. Roll back by
unsetting the variable or setting `off`; the merge-group full fix remains.
Reverting planner code requires a reviewed revert that retains that fix. The
workflow shape change (static-layout gates on `go-test`, `go-timing`,
`web-shard`, `e2e-run`; the `layout` output; layout-aware aggregates) is inert
while the variable is off because the planner then never emits `static`.

The historical shard inventory selected 53 gated specs: the original
49 measured specs plus `clip-tip`, `aeon-632b-clip`, `key-trim` and `model-prefs`, whose
weights are scheduling estimates. `clip-tip` uses its complete local serial
runtime plus a 20 percent margin; the other three use listed test counts.
These are not hosted runtime measurements. The one-worker tier runner now uses
serial estimates for historical N-worker steps (`N * weightSeconds`); it retains
local serial and unmeasured estimates. The committed full-gate ledger estimates
368–372 seconds per browser shard (max/median 1.01), versus 266–497 seconds with
the old allocation evaluated under the same serial model. Original exact-spec
weights, launch policies, tier classifications and optional catalogue membership
stay unchanged. These estimates exclude collection, launch and runner overhead;
the 12–13 minute PR target still requires hosted measurement. The 164 specs of the
`remaining-ui` group are declared in
`web/ci-web-shards.json` with `gate: false` and run only with `--all`, until a
follow-up ticket measures them. Unit CI checks the source manifest before
reconciliation: every spec (including nested files) must belong to a group or
an explicit exclusion with a ticket key and reason. AEON-676 fixes the touch
name-width shift and gates all four additions in `clipped-names-and-preferences`,
with no `clip-tip` exclusion. Browser-free regression checks verify the hosted
workflow wiring, exact-once default selection and propagation of each spec's
failure. The layout fix passed six variants × 20 locally; hosted execution of
the integrated revision must still be verified by the coordinator. Inspect drift
without browsers using `npm --prefix web run ci:web:shard -- --check --strict`.

**AEON-676 hosted acceptance is open:** the earlier 2026-10-04 read-only
GitHub check found no commit `42537130c7488f43f6899f1377cd6bbbc964bd33`
(HTTP 422), no `work/aeon-676` ref (HTTP 404), no PR and zero Actions runs
for that SHA. The FIX6 check now confirms that the coordinator published
`2e8c089c3e863fd8d2e2a05cae0ddef41dbdce66` on
[PR #257](https://github.com/inspr-at/paimos/pull/257). Its
[CI run 37176759250](https://github.com/inspr-at/paimos/actions/runs/37176759250)
(attempt 1, `pull_request`, that exact head SHA) has cancelled `web-setup`
and `web-shard` jobs and a queued `web` aggregate at observation time. The
setup annotation says a higher-priority request for `ci-pull_request-257`
superseded it; no successful setup or 12-shard execution establishes acceptance.
The worker's read-only snapshot is `tmp/aeon-676/fix6/hosted-evidence.json`.
Post-merge local checks pass: hosted wiring 2/2, shard integration 19/19 and
signal/failure guard 49/49, all without skips; strict coverage remains 53 gated
and 164 ungated specs across 12 shards. No behavior or assertion was changed.

The worker is forbidden to push; local checks do not close this gate. The
coordinator must complete `CI` on the integrated branch (PR or branch dispatch,
keeping mbp2606 off limits). Retain the run URL, checked-out SHA, logs/reports
showing `clip-tip`, `aeon-632b-clip`, `aeon-632b-clip-settings`, `key-trim` and `model-prefs` actually
executed without skips, and successful `web-setup`, all 12 `web-shard` jobs
and the required `web` aggregate. A later revision must retain these fixes
and identify its exact SHA; selection-only output or a run on an unrelated
revision does not establish hosted acceptance. FIX6 completes only the worker's
verification and handoff; review-cg27's hosted finding remains open.

A flake retry (`CI_FLAKE_PLAYWRIGHT_TESTS`)
reruns exactly one test in its original config group.
Signal termination fails the guard before quarantine or test-output handling
on either attempt; a terminated retry stops the remaining retries. Failure
evidence records the signal, even when captured output reports passing or
quarantined tests.
The AEON-676 FIX5 check confirmed that fix `40f5b53f` survives the main merge
at `2a6a0803`: all nine signal regressions fail against the reviewed
`2c51fcd0` implementation, while the guard's 49 tests and the shard integration's
21 tests pass after the merge. Strict manifest validation also passes. These
local checks leave the hosted acceptance requirement above open.

**Active and required admission contract: mode B (Free plan), decided by Markus
on 2026-09-30 and recorded on NIX-600.** The implementation references below
are pinned to [nixcfg #890](https://github.com/markus-barta/nixcfg/pull/890) at
`5e304365cad08794fc839487c8a4512928d738cd`; module filenames mean
`modules/aeon-builder/`, and test filenames mean `tests/`. These are source
references, not live acceptance evidence. Publishing
`AEON_MBP2606_AVAILABILITY` remains gated on coordinator verification of all
three controls and their integration. A JIT registration is not a reservation
for the checked job: even a base-only job fits the runner's labels
([aeon_builder.py:93–100](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L93-L100), [test_aeon_builder.py:615–624](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/tests/test_aeon_builder.py#L615-L624)).

1. **Verified JIT minting:** `tick` selects queued jobs carrying `mbp2606` and
   verifies the repository and head repository, allowed event, workflow path,
   `main` head branch and head SHA reachability; missing or mismatched run
   metadata is rejected
   ([aeon_builder.py:65–86](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L65-L86), [aeon_builder.py:645–675](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L645-L675)).
   The configured repository is `inspr-at/paimos`, events are `push` and
   `workflow_dispatch`, and workflow paths are `.github/workflows/ci.yml` and
   `.github/workflows/test-runner-smoke.yml`
   ([default.nix:107–110](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L107-L110), [default.nix:137–155](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L137-L155)).
   `claim_slot` records job ID, run ID/attempt and event; `serve` records a
   unique runner name before requesting its JIT configuration
   ([aeon_builder.py:811–828](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L811-L828), [aeon_builder.py:867–872](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L867-L872)).
   `class_ok` requires exactly the verified event's class from the configured
   pair: `mbp2606-push` for `push`, `mbp2606-dispatch` for `workflow_dispatch`
   ([aeon_builder.py:103–117](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L103-L117), [default.nix:125–143](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L125-L143)).
   At candidate selection, a missing, opposite or doubled class triggers a
   cancellation attempt and prevents that run from being served
   ([aeon_builder.py:676–684](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L676-L684), [test_aeon_builder.py:615–631](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/tests/test_aeon_builder.py#L615-L631)).
   Before **every mint**, `serve` passes the prospective runner's full labels
   to `unverified_label_runs` and refuses that mint if the sweep finds a
   rejected run ([aeon_builder.py:855–869](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L855-L869)).
   The sweep examines queued jobs whose labels fit that set through
   `could_take`; each matching job must belong to a verified run and pass
   `class_ok`, **including jobs of already verified runs**
   ([aeon_builder.py:899–929](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L899-L929)).
   It attempts to cancel runs failing either check, including base-only jobs
   such as `[self-hosted, Linux, ARM64, mbp2606]`, `self-hosted`,
   `[self-hosted, linux]` or `ARM64`
   ([aeon_builder.py:909–929](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L909-L929), [test_aeon_builder.py:595–609](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/tests/test_aeon_builder.py#L595-L609), [test_aeon_builder.py:633–641](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/tests/test_aeon_builder.py#L633-L641)).
   Cancellation uses `actions:write`; JIT registration uses
   `administration:write` ([aeon_builder.py:44–51](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L44-L51), [aeon_builder.py:366–376](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L366-L376)).
   The runner starts for one job inside the VM, with VM retirement and runner
   deregistration in the controller's cleanup path
   ([provision-base.sh:41–68](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/provision-base.sh#L41-L68), [aeon_builder.py:875–897](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L875-L897)).

2. **Job-started hook:** the sealed image installs the executable hook at
   `/opt/aeon/job-started.sh`, outside the checkout and runner directory, and
   sets `ACTIONS_RUNNER_HOOK_JOB_STARTED` to that path
   ([provision-base.sh:23–54](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/provision-base.sh#L23-L54)).
   The hook checks repository, event, `GITHUB_REF`, workflow ref and payload
   metadata against the baked allowlist; its fully qualified workflow refs
   are derived from the configured repository, paths and `refs/heads/main`
   ([job-started.sh:33–48](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L33-L48), [aeon_builder.py:227–234](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L227-L234), [default.nix:107–155](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L107-L155)).
   **PR events and PR-shaped payloads are denied**, including same-repository
   PRs: the allowlist has only `push` and `workflow_dispatch`, and the payload
   must have the expected repository and no `pull_request`
   ([default.nix:137–143](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L137-L143), [job-started.sh:37–48](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L37-L48)).
   The hook checks event and payload metadata, not class labels
   ([job-started.sh:33–58](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L33-L58)).
   **Class binding is `class_ok` at candidate selection and in the pre-mint
   sweep**, not the post-job check
   ([aeon_builder.py:676–684](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L676-L684), [aeon_builder.py:857–860](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L857-L860), [aeon_builder.py:912–928](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L912-L928)).
   On denial, the production hook records an error, freezes `Runner.Listener`
   and `Runner.Worker` with `SIGSTOP`, invokes `poweroff -ff` and waits without
   returning; freezing avoids systemd reaping the hook before power-off
   ([job-started.sh:17–31](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L17-L31)).
   A permitted hook writes the admission marker and waits for `cache-ready`
   ([job-started.sh:50–58](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L50-L58)).
   The cache remains locked at runner startup; the controller waits for that
   admission marker, attributes `runner_name` through the API and requires
   membership in `verifiedRuns` before unlocking and mounting the cache
   ([start-runner.sh:2–6](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/start-runner.sh#L2-L6), [aeon_builder.py:1017–1023](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1017-L1023), [aeon_builder.py:1037–1067](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1037-L1067), [cache-lock.sh:19–48](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/cache-lock.sh#L19-L48)).
   The separate `admit` cache safeguard refuses to unlock a trusted push disk
   for another event; it checks the actual run event, not class labels or the
   minted job ID/attempt ([aeon_builder.py:1053–1067](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1053-L1067)).
   NIX-600 smoke acceptance must still demonstrate that rejection prevents
   workflow-step output and cache writes, including an `if: always()` step
   and an action with a `pre:` step. The deny and cache-wait mechanisms above
   are implementation references, not proof of that acceptance result.

   GitHub's [job hook documentation](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts)
   says "the job will not run"; this contract explicitly **does not rely on
   that claim**. In actions/runner at `ca43437862b6d6be24e6de73dff3971c99140c9a`,
   the hook is an ordinary `always()` pre-job step
   ([JobExtension.cs:302–310](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/JobExtension.cs#L302-L310)).
   A failed step only updates the job result
   ([StepsRunner.cs:274–278](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/StepsRunner.cs#L274-L278));
   later step conditions are still evaluated
   ([StepsRunner.cs:203–241](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/StepsRunner.cs#L203-L241)),
   and action `pre-if` defaults to `always()`
   ([ActionManifestManager.cs:458](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/ActionManifestManager.cs#L458)).

3. **Controller post-job check:** `find_runner_job` queries the expected job
   through the API, then searches jobs across all attempts of the last 20
   verified run IDs and up to 30 recent runs for the minted `runner_name`
   ([aeon_builder.py:350–360](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L350-L360), [aeon_builder.py:1069–1081](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1069-L1081)).
   Step 3 accepts attribution to a run in `verifiedRuns`, **including a
   sibling job of that run**; it does **not** re-check class labels or compare
   the actual job ID/attempt with the values recorded before minting
   ([aeon_builder.py:1083–1093](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1083-L1093), [test_aeon_builder.py:518–522](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/tests/test_aeon_builder.py#L518-L522)).
   If the runner cannot be attributed or the actual run is not in
   `verifiedRuns`, it calls `pause`, which records an alert, stops minting,
   attempts to clear availability and cancel matching queued runs
   ([aeon_builder.py:1083–1091](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1083-L1091), [aeon_builder.py:615–635](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L615-L635)).
   `serve` invokes this host-controller check after work and taints the slot
   when it fails; its `finally` path retires the VM and removes the runner
   registration independently of a job completion hook
   ([aeon_builder.py:876–897](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L876-L897)).

**Deny recording:** the hook writes a deny line to its log and an error to job
stderr ([job-started.sh:17–21](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L17-L21)).
While monitoring work, the controller treats VM power-off or a hook deny line
as denial and pauses; its hard-stop check takes precedence
([aeon_builder.py:1002–1016](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1002-L1016)).
Denied, unverified and unattributable outcomes taint the slot
([aeon_builder.py:876–883](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L876-L883)).
Actual-job attribution uses API job/run evidence for `runner_name`, rather than
trusting the job's writable VM logs
([aeon_builder.py:1060–1081](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1060-L1081)).

The configured controller and baked hook allow only **`push`,
`workflow_dispatch` on main**
([default.nix:137–155](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L137-L155), [aeon_builder.py:65–86](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L65-L86), [aeon_builder.py:227–234](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L227-L234), [job-started.sh:37–42](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/job-started.sh#L37-L42)).
`ci.yml` uses that same event/ref allowlist. `schedule`, `merge_group` and tags
are excluded. Expanding events or refs requires a reviewed change to the
controller, hook configuration and workflow guard.

**Ruleset precondition:** the configured main ruleset is **24240960** with a
pinned expected baseline ([default.nix:249–261](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L249-L261)).
The controller compares active enforcement, target, ref conditions, bypass
actors and rules (including parameters), using a fresh API read before each
availability publish and mint; drift calls `pause`
([aeon_builder.py:136–158](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L136-L158), [aeon_builder.py:369–372](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L369-L372), [aeon_builder.py:637–643](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L637-L643), [aeon_builder.py:737–750](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L737-L750), [aeon_builder.py:855–860](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L855-L860)).
The ruleset API call uses `administration:write` so the response includes
bypass actors; that permission is not itself evidence of intact protection
([aeon_builder.py:369–372](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L369-L372)).

**Mode-B runtime:** `serve` clones and starts a fresh job VM from the sealed
rootful-Docker base; base configuration disables host mounts and SSH agent
forwarding, and the runner download targets Linux ARM64
([default.nix:75–79](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L75-L79), [aeon_builder.py:189–205](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L189-L205), [aeon_builder.py:843–847](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L843-L847), [provision-base.sh:23–32](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/provision-base.sh#L23-L32)).
The provisioned runner has Docker access and passwordless sudo, making the VM
the isolation boundary ([provision-base.sh:16–21](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/provision-base.sh#L16-L21)).
Controller retirement deletes the VM before settling its disk or releasing
its slot; a failed deletion or disk settlement holds the slot and pauses
([aeon_builder.py:767–782](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L767-L782)).
Persistent slot caches are LUKS2 containers, unlocked and mounted only after
hook admission and verified-run attribution; trusted slot disks are for pushes,
while dispatches receive disposable copies
([cache-lock.sh:2–8](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/cache-lock.sh#L2-L8), [cache-lock.sh:19–48](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/cache-lock.sh#L19-L48), [aeon_builder.py:1017–1067](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1017-L1067), [default.nix:156–160](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L156-L160), [aeon_builder.py:945–966](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L945-L966)).
Existing disks attach with `format:false`; only newly created disks permit
formatting and cache initialization with `--init`
([aeon_builder.py:175–186](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L175-L186), [aeon_builder.py:945–966](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L945-L966), [aeon_builder.py:1061–1064](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1061-L1064), [cache-lock.sh:28–37](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/cache-lock.sh#L28-L37)).
A tainted trusted disk is restored from its last known-good APFS clone, or
removed if no such clone exists; an untainted used trusted disk becomes the
next known-good copy after the external check, and scratch disks are discarded
([aeon_builder.py:876–897](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L876-L897), [aeon_builder.py:968–996](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L968-L996)).

**Network precondition:** host `pf` rules block configured private ranges and
host loopback for user `ci`, except the stateless Lima SSH loopback range from
`sshPortBase - 1` through `sshPortBase + slots - 1`
([aeon_builder.py:208–224](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L208-L224), [default.nix:222–235](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L222-L235), [hosts/mbp2606/home-ci.nix:24–35](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/hosts/mbp2606/home-ci.nix#L24-L35)).
The pinned defaults provide four job slots plus one base/proof port; the base
and proof use the preceding port, with no host mounts or SSH agent forwarding
([default.nix:162–165](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L162-L165), [default.nix:191–200](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L191-L200), [aeon_builder.py:175–205](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L175-L205), [aeon_builder.py:573–586](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L573-L586)).
The rule generator documents hostagent DNS resolution through
`mDNSResponder`; it blocks private destinations, not all port-53 egress, so
host-resolver DNS remains a residual risk
([aeon_builder.py:208–224](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L208-L224)).
The controller probes network reachability at `on`, before each mint, and on
periodic idle proofs (ten minutes by default), rather than inspecting the
anchor before every availability publish
([aeon_builder.py:547–586](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L547-L586), [aeon_builder.py:1222–1240](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1222-L1240), [aeon_builder.py:849–854](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L849-L854), [aeon_builder.py:707–725](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L707-L725), [default.nix:207–215](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L207-L215), [aeon_builder.py:737–750](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L737-L750)).
A failed per-job or periodic proof pauses the pool
([aeon_builder.py:719–723](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L719-L723), [aeon_builder.py:849–854](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L849-L854)).
Coordinator network-isolation acceptance is required before enabling the pool.

Org state verified on 2026-09-30: only the **Default** runner group, public
repositories not allowed, **0 runners**; Blacksmith is removed.

Mode A is only a possible future Team-plan upgrade: a group restricted to paimos
and the two selected main workflow refs, recorded and verified before adoption,
retaining the baked job-started hook as defence in depth.

Fork-PR approval is `all_external_contributors` (set by the lead, 2026-09-30).
That is defence in depth, not the runner admission boundary. A `merge_group` run
executes PR code, so queueing a PR is a decision to run it on the Mac if routing
is ever enabled for that event. **It is excluded from the mbp2606 allowlist in
mode B today**: GitHub documents exact pinned workflow refs; matching
`gh-readonly-queue/…` refs to the selected `main` workflows is unverified.
Merge-queue CI continues on hosted runners. See GitHub's
[runner-group workflow restrictions](https://docs.github.com/en/enterprise-cloud%40latest/actions/how-tos/manage-runners/self-hosted-runners/manage-access).

Routing is disabled until NIX-600's controller publishes the repository variable
`AEON_MBP2606_AVAILABILITY` on `inspr-at/paimos` with this value-free shape
([aeon_builder.py:161–172](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L161-L172), [aeon_builder.py:388–404](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L388-L404)):

```json
{"schema":1,"repository":"inspr-at/paimos","os":"linux","arch":"arm64","online":true,"busy":false,"observed_at":"2026-09-30T10:00:00Z","idle_runners":4}
```

The schema remains **version 1**; mode and rerun-attempt metadata require no new
availability fields. In mode B, `idle_runners` counts free VM slots after
occupied slots and pending jobs are subtracted, not idle registered runners
([aeon_builder.py:161–172](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L161-L172), [aeon_builder.py:607–616](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L607-L616)).
The publisher waits `min(5, pollSeconds)` seconds between publication attempts;
publication requires mode `on` and a fresh ruleset check, and represents
exhausted capacity with `busy:true`
([aeon_builder.py:161–172](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L161-L172), [aeon_builder.py:727–750](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L727-L750)).
`off` changes mode and attempts to clear availability under the same state
lock as publication ([aeon_builder.py:737–750](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L737-L750), [aeon_builder.py:1268–1279](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1268-L1279)).
Records expire after **30 seconds**. An absent, invalid, expired, future-dated,
offline or busy record selects hosted immediately;
there is no network wait and no runner administration credential in CI. GitHub's
[runner-list API](https://docs.github.com/en/rest/actions/self-hosted-runners#list-self-hosted-runners-for-a-repository)
requires repository Administration read access; that belongs to the host
controller, not the workflow token. Neither token permissions nor environment
secrets are added here. The Linux ARM64 pool must provide Docker service-container
support, Ubuntu-compatible `apt`/`sudo`, Go 1.26 and the shells used by the tests.
Routable `go` and future routed `e2e` jobs must not assume amd64:
`pgvector/pgvector:pg18` is multi-arch, and setup-go/setup-node select ARM64 on
this runner. The guard rejects routed jobs referencing `amd64`, `x86_64`,
`x86-64`, `i[3-6]86` or the token `x64` in artifacts, including action inputs,
services, matrices and inherited environment/default settings. `release.yml`
and `pairing-platform.yml` are **never routed**; their multi-platform artifacts
and evidence remain hosted.

**Off/drain:** with a running controller, `off` attempts to clear availability
and enters `draining`; `tick` keeps serving verified queued jobs carrying
`mbp2606` and passing
`class_ok`, with the same mint checks, until candidates and active slots/workers
are gone ([aeon_builder.py:1268–1287](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1268-L1287), [aeon_builder.py:645–705](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L645-L705), [aeon_builder.py:849–869](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L849-L869)).
**Pause:** a ruleset/network failure or failed post-job attribution calls
`pause`, stops minting and attempts to clear availability and cancel runs with
queued jobs matching the combined `pool_labels` by case-insensitive subset
([aeon_builder.py:615–643](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L615-L643), [aeon_builder.py:719–723](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L719-L723), [aeon_builder.py:849–854](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L849-L854), [aeon_builder.py:1083–1091](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1083-L1091), [aeon_builder.py:1149–1157](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1149-L1157)).
`pool_labels` contains the base labels plus **both** `mbp2606-push` and
`mbp2606-dispatch` ([aeon_builder.py:126–128](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L126-L128), [default.nix:115–130](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L115-L130)).
**Hard stop:** with a running controller, `off --now` enters `stopping` and
attempts to clear availability and cancel runs with queued or in-progress jobs
matching that same combined set; monitored workers stop and retire their VMs
([aeon_builder.py:1275–1285](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1275-L1285), [aeon_builder.py:1149–1157](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1149-L1157), [aeon_builder.py:1007–1010](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1007-L1010), [aeon_builder.py:889–897](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L889-L897)).
A lease is an admission check, not an atomic reservation: concurrent
admissions or host failure after selection remain a queue risk. Never publish
a simple persistent `on` flag. Offline/busy smoke and full-suite timing must be
recorded when the host becomes available.

For routed workflows, use **Re-run all jobs** (`gh run rerun RUN_ID` without
`--failed`) so the hosted router refreshes the lease. It emits `run_attempt`;
consumers compare it with `github.run_attempt` and select hosted if a failed-job
or individual-job rerun retains an older output. This rejects stale attempts,
not a lease that expires after initial job scheduling; the controller's draining
duties cover already queued jobs ([aeon_builder.py:645–705](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L645-L705)). See GitHub's
[rerun behavior](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs).

**Go shard integration (AEON-459):** `go-test` depends on `runner-route` and
uses its guarded runner selection and class to choose **4 Go shards on
mbp2606, 7 on hosted**. The same event/ref/attempt guards protect the matrix
when GitHub evaluates it. A failed-job or individual-job rerun may retain
earlier matrix values; do not assume it creates seven hosted shards. Use
**Re-run all jobs** to refresh both the router and the entire shard layout.
`scripts/ci-go-shards` accepts `-count` (default 7). Its checked-in plans are
`scripts/ci/go-shards.txt` and `scripts/ci/go-shards-4.txt`; both retain the same
timing weights and inventory, including `scripts/ci-runner-guard`. Static
coverage checks prove exactly-once execution for both layouts, including new
packages and tests, with timing tests excluded from the parallel commands.
The **Timing budgets, alone** step runs once in its own `go-timing` job on
`ubuntu-latest`, where its budgets were calibrated. It is unconditional for
both routes and required by `go`; moving it out of hosted shard 4 prevents
reruns or a route switch from skipping it. The required check names remain
`go`, `web`, `release-check` and `e2e`.
Every Mac shard sets **`GOFLAGS=-count=1`** and records its actual runner class,
source commit and event. Routed checkout uses **`persist-credentials: false`**,
and `setup-go` cache is enabled only on main pushes. Insufficient capacity
sends the entire batch to hosted; broader routed fan-outs must request their
whole simultaneous capacity through `required-idle-runners`.

**Measurement gate:** the pool stays off during worker validation. The lead
must record five successful Mac runs and five successful hosted runs of the
same commit and event on AEON-459, with run IDs and median Go-phase wall time.
Measure from the first start of `go-test`, `go-static` or `go-timing` to the
completion of the required `go` aggregate; the aggregate's own short duration
does not measure the tests. Include queue delay within that phase. Keep this
route only if the Mac median is lower; otherwise retain hosted routing.

Every evidence-producing Go test on mbp2606 uses **`go test -count=1`** to bypass
cached test results. Routed action caches are written only by main pushes.
The controller's persistent Go, npm and Playwright slot cache is for verified
pushes; dispatches receive disposable scratch copies of known-good
([provision-base.sh:50–54](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/provision-base.sh#L50-L54), [default.nix:156–160](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L156-L160), [aeon_builder.py:945–966](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L945-L966), [aeon_builder.py:1053–1067](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L1053-L1067)).
NIX-600 must enforce that isolation. The routed workflows disable `setup-go`
caching outside main pushes. A test cache hit is not fresh evidence.

`go run ./scripts/ci-runner-guard` scans **all** workflow YAML, including `.yml`
and `.yaml` in any letter case. Hosted labels are exactly `ubuntu-latest`,
`ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15` and `macos-15-intel`. The
controller mints only the configured base labels plus the verified event class
([default.nix:115–143](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/default.nix#L115-L143), [aeon_builder.py:120–123](https://github.com/markus-barta/nixcfg/blob/5e304365cad08794fc839487c8a4512928d738cd/modules/aeon-builder/aeon_builder.py#L120-L123)).
The guard's fixture tests reject
direct labels, hosted-looking impostors, unsafe expressions, matrix labels,
unguarded router outputs, routed jobs with secrets/environments/write permissions,
amd64 artifact references, and routed release/pairing/image/attestation/pin jobs.
Unknown dynamic expressions fail closed.
The hosted `release-check` runs both guard and router tests. Release workflows,
image build/relink, attestation and pin gates always stay hosted; attestation
verification must retain `--deny-self-hosted-runners` in its owning gate.

The lead accepted mbp2606 green evidence for tree-keyed reuse of **tests/evals**
only (AEON-438, 2026-09-30). Successful routed jobs record their actual
`runner_class=hosted|mbp2606`, source commit and event in the job summary. Any
future reuse record must preserve that class. Image provenance, attestations and
pin gates may never reuse that evidence. This change adds no tree-skip mechanism.

A push of an annotated `v*` tag runs `.github/workflows/release.yml`. The coordinator creates it with `node scripts/create-release-tag.mjs --write`, which first requires the exact-main-SHA rehearsal receipt; lightweight tags fail before the image build (AEON-398).

The server image pipeline starts independently of the macOS jobs (AEON-407, AEON-504). **Every release requires linux/amd64 and linux/arm64**, built and smoked natively in parallel; either platform failing prevents release-index publication. GitHub's hosted `ubuntu-24.04` runner builds amd64 and `ubuntu-24.04-arm` builds arm64 ([runner labels](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)). No QEMU is installed, and each job checks `uname -m` before building.

1. Each `image-platform` matrix job checks out all tags, validates the calendar coordinate and presentation bundle, and requires an annotated tag matching `version.json`. It refuses an existing GitHub release (including drafts) or GHCR release tag; lookup errors fail closed.
2. Generate the release-history manifest once. Freeze the source commit timestamp as `SOURCE_DATE_EPOCH`; compile web and Go on the native host. The npm cache is restored (every tarball is checked against the lockfile); **no Go cache is restored**, and Go compiles cold from modules verified against `go.sum` (see [Cold Go compilation](#cold-go-compilation-for-shipped-binaries)). `scripts/build-image-inputs.mjs` runs the production prebuild checks, uses `-trimpath -buildvcs=false -tags webembed`, disables CGO, and injects the exact reserved version. The input manifest records the tree, toolchain versions, generated-history hash, web hashes and binary hash; the final executable has mode 755 and a fixed mtime.
3. Prepare the runtime closure cold: build `scripts/Dockerfile.runtime` (the digest-pinned Alpine base plus the pinned `chromium` and `tini`) with `no-cache` and without any registry cache, in every tag build and every rehearsal (see [Runtime closure](#runtime-closure-prepared-cold-on-every-build)). Bind the exported OCI runtime manifest digest as the `aeon-runtime` named context and print it per architecture. The main Dockerfile only copies `dist/image-input/paimos` and `NOTICE`, retaining UID/GID 65532 and the tini entrypoint. Import the existing `:buildcache` or `:buildcache-arm64` assembly cache and load the native image. Resolve its tag with `docker image ls --quiet --no-trunc`, require exactly one full ID, then run the full smoke gate on that immutable ID. Both assembly and export use the same working-directory context, base digest and single-platform arguments, with `rewrite-timestamp=true` and OCI media types.
4. Record assembly plus smoke timing and require two clean same-input rebuilds (below). After these gates pass, export the same image inputs with BuildKit `provenance: mode=max` and push **by digest only**. Update that architecture's assembly cache in `mode=max`. Platform jobs never assign the release tag. Before anything is attested, `scripts/verify-pushed-image.mjs` reads the pushed digest back, walks to its `linux/<arch>` manifest and requires the image config digest of the smoked build; a mismatch fails the job and prints both digests. Each job then creates and verifies a GitHub build-provenance attestation bound to its digest, repository, workflow, source tag and commit, and uploads its digest artifact. That handoff is the job's last step.
5. The `image` job waits for both platform jobs to succeed, rechecks release/tag immutability, and combines their immutable references using `docker buildx imagetools create`. Before publishing and after reading back, require exactly linux/amd64 and linux/arm64 runtime manifests with separate bound BuildKit provenance descriptors. Publish one multi-arch OCI index at `ghcr.io/inspr-at/aeon:<version>`, without a `latest` alias.
6. Attest and verify the index digest too. Preserve `needs.image.outputs.version` and `needs.image.outputs.digest`; **digest is the index digest**, also recorded in the summary and draft release notes. The notes keep exactly one `Digest:` line (the index, which `scripts/verify-live.mjs` requires) and add one `Runtime closure linux/<arch>:` line per architecture. Production **csb1 is x86_64 and automatically pulls linux/amd64 from this index**. ARM servers and Apple-silicon Linux VMs pull linux/arm64 from the same tag or digest, without emulation.

The index is the deployment and rollback pin. Partial by-digest platform exports are untagged and cannot be deployed through the release coordinate. Once an index tag has been published, a later failure still requires a new coordinate; reruns cannot replace it. Real registry push/attestation and deployment verification remain coordinator release gates.

In parallel, macOS runners build darwin `paimos-agentd` with CGO enabled, then sign it with Developer ID (team P66J39QV6V, hardened runtime) and notarize it in the `release-signing` environment before upload (docs/AGENT_INTEGRATION.md, Signed release daemon). The `assets` job waits for both signed darwin targets and the verified image job, builds Linux `paimos-agentd` and all `aeon-cli` targets statically (cold, like every shipped Go binary), verifies the darwin binaries and computes `SHA256SUMS` over all eight binaries. It rechecks release immutability, then creates one **draft** GitHub release with all nine assets and the image digest (AEON-356). Existing drafts and published releases are never uploaded to or overwritten. A partial image publication requires a new coordinate rather than a rerun that replaces it.

Publication remains the coordinator's explicit step after successful native agent qualification (AEON-487), before the server switch (AEON-493). Failed or incomplete artifact checks or native qualification leave the release a draft. The tap must be merged and its exact public assets checked before switching the server. Server deployment still requires its separate approval and live verification of the exact image digest, version and health. After publication, the coordinator dispatches `.github/workflows/homebrew-tap.yml` on **main**, passing only the version as data. Release events cannot receive tap secrets: their workflow definition can come from an untrusted tag. The trusted main job requires the exact published immutable release, annotated tag on main, complete digest-pinned assets and `SHA256SUMS` bytes matching the API asset digest before minting a tap token. It renders `Formula/aeon-agentd.rb` from those verified bytes and opens a pull request on `inspr-at/homebrew-tap` using the protected environment's App identity. The formula installs the signed, notarized darwin bytes with `bin.install` and does not rebuild or re-sign them. If either App secret is absent the bump logs `homebrew tap bump skipped: app secrets absent` and succeeds; environment preflight still requires installed protection. The stable 105 sample is [docs/homebrew/aeon-agentd.rb](homebrew/aeon-agentd.rb).

To verify a published image independently, use its exact digest and source commit:

```sh
gh attestation verify "oci://ghcr.io/inspr-at/aeon@$DIGEST" \
  --repo inspr-at/paimos \
  --signer-workflow inspr-at/paimos/.github/workflows/release.yml \
  --source-ref "refs/tags/v$VERSION" --source-digest "$COMMIT" \
  --deny-self-hosted-runners
```

The attestation action uses the existing `packages`, `attestations` and OIDC write scopes only in the platform and index jobs. Storage-record creation is disabled so no `artifact-metadata` write scope is needed. Both image jobs retain the existing `contents: write` permission so their immutability lookups can see drafts (GitHub restricts draft listings to push access). Only `assets` creates the draft release; signing stays in its existing environment. Every action in these image/release workflows is pinned to a commit.

### Deployment pin proposals (AEON-413)

Immediately after the multi-arch index attestation passes, the `image` job
runs `scripts/release-pin-pr.mjs`. The only foreign-repository write path this
bot may use is a **draft PR** against `markus-barta/nixcfg` `main`, changing
exactly the Aeon image line in `hosts/csb1/docker/compose-spec.nix`. It preserves
the rest of the file, including comments. This implements AEON-413 as a proposal;
the worker brief supersedes the older ticket's `--auto` request. The bot never
enables auto-merge, merges, approves, pushes to main, publishes or deploys.
PMA and other deploy repositories have no bot write path here; their owning
coordinator receives a proposed diff and follows their own tracker and gates.

Before target authentication or branch creation, the bot requires the exact
repository/tag-push invocation, an annotated tag resolving directly to the
workflow's source commit, and that commit's ancestry on current source main.
It independently runs `gh attestation verify` for the index digest, binding
`inspr-at/paimos/.github/workflows/release.yml`, the exact source tag and commit,
the exact tag-scoped certificate identity and signer commit, and
`--deny-self-hosted-runners`. Missing attestations, lightweight/off-main
tags and failed or ambiguous API reads fail closed before any target write.
It rejects older versions and conflicting digests for an existing coordinate.

Default mode is read-only. With App credentials it requests a contents-read
installation token and prints the one-line diff. Without those credentials it
records a held proposal explicitly; the lead must provide a read-only snapshot
of the pin file to obtain the diff. A local snapshot can be used with
`node scripts/release-pin-pr.mjs --pin-file SNAPSHOT.nix`, retaining the same
live source/attestation checks; it can never be combined with write mode.
Set the ordinary release inputs `VERSION`, `DIGEST`, `GITHUB_SHA`,
`GITHUB_REPOSITORY`, `GITHUB_EVENT_NAME=push`, `GITHUB_REF=refs/tags/v<version>`
and a process-only `GH_TOKEN`; never pass credentials on the command line.

Coordinator activation requires the release-only `release-pinning` environment,
its `AEON_PIN_APP_ID` / `AEON_PIN_APP_KEY` secrets, and repository variable
`AEON_PIN_BOT_ENABLED=true`. Protect this environment and tag creation so only
the approved release coordinator/App can cut trusted release tags. The pin App
must be non-admin, installed only on nixcfg, with contents and pull-request
access; token minting further narrows it to `nixcfg` and exactly contents/write
plus pull_requests/write. It receives no settings, review bypass or production
credentials. Tokens are revoked on success and failure. No credentials,
environment, App permissions or repository settings are provisioned by this
change. Before cutting the tag, the coordinator records the pin automation's
canonical worker marker and release scope in the designated owning tracker;
the App receives no tracker credential (D7). Missing write credentials fail
rather than silently claiming a PR.

Write mode requires both `--write` and the enable flag. The branch
`aeon-pin-v<version>` starts at an exact observed nixcfg main SHA. The bot
checks the committed comparison before proposing: one commit with that sole
parent, one modified file, one added/deleted line and the exact intended blob.
Retries reuse only the same verified draft PR; they never overwrite an existing
branch, reopen a closed PR or change a coordinator's ready-for-review state.
Main advancing during a retry may require coordinator resolution instead of
silently rebasing or sweeping in other changes. API failures can leave an
unmerged proposal branch; only its owner may clean it up after inspection.

The PR records the source commit, digest and verification, exact base SHA/file
(the immutable pre-change backup reference) and previous image pin for rollback.
The same evidence goes to the image job summary and, when the independent assets
job assembles it, the existing draft paimos release's notes. There is no GitHub
release yet during the image job, so the bot does not race its creation or
rewrite an existing release. Before any pin merge or rollout, the coordinator
must complete the owning nixcfg checks/review and record the validated database
backup required by the release procedure. Rollback remains a separately
reviewed change to the recorded previous immutable image; the bot never performs
it. Disable proposal writes by clearing the enable variable.

Fixtures run in `release-check` and the existing hosted image dry run; they make
no foreign writes. The target of PR-open within 30 seconds after index push
requires a coordinator-authorized release run with timestamps, a provisioned App
and the nixcfg gates. The workflow records UTC immediately after successful
index push and the bot records GitHub's PR creation time and elapsed seconds in
the evidence. Fixture timings are not live acceptance evidence.

### Image dry runs and timing evidence

`.github/workflows/release-image-check.yml` supports `workflow_dispatch` and draft-PR validation of the release workflow, smoke script and Dockerfile. Its two native hosted matrix jobs run the workflow/index and assembly-script regression tests, generate offline release history once, compile externally with the tag build's exact Go and npm cache settings, read the matching architecture's assembly cache, prepare the runtime closure cold and freeze it by digest, load that platform's production assembly and run the same full smoke gate. Both must also produce complete assembly/smoke timing evidence and pass the two-clean-rebuild digest proof. The 90-second target is an AEON-422 acceptance measurement; slower successful samples warn without blocking releases. The rehearsal exports production BuildKit provenance locally with the identical digest-bound base and inputs, then requires that export to carry the smoked image config, using the code that checks the pushed digest in a tag build. Its token has only `contents: read`; it has no signing environment, registry login, registry cache export, image push, attestation or release creation. Before tagging, require the successful exact-SHA `main` receipt described in [Mandatory pre-tag rehearsal (AEON-531)](#mandatory-pre-tag-rehearsal-aeon-531); a work-branch or PR run is diagnostic only and cannot authorize a release tag. Hosted timing can be measured in rehearsal; real attestation verification still requires a coordinator-authorized publishing run. A local fixture test establishes neither.

### Host compilation, runtime pin and digest proof (AEON-422)

`scripts/Dockerfile.runtime` pins the official Alpine 3.24 multi-platform index
by digest (3.24.2 when observed on 2026-10-05), while preserving Chromium
152.0.7977.82-r0 and tini 0.19.0-r3. The index keeps both native architectures;
the smoke gate still checks their installed pins, licenses, NOTICE, UID/GID,
mounted files, embedded web, authenticated APIs and Chromium PDF rendering.
The runtime closure uses epoch 0, so neither the source commit's timestamp nor
the build time enters it. Every tag build and every rehearsal prepares it cold;
it is never restored from or saved to a cache
([Runtime closure](#runtime-closure-prepared-cold-on-every-build)).

Every assembly targets exactly one native platform and omits
`BUILDKIT_MULTI_PLATFORM=1`. Hosted rehearsal run
[37340960096](https://github.com/inspr-at/paimos/actions/runs/37340960096)
failed on both architectures after the COPY steps with
`docker exporter does not currently support exporting manifest lists`.
That flag forces a multi-platform result even for one target; the
[BuildKit 0.33.1 Docker exporter](https://github.com/moby/buildkit/blob/v0.33.1/exporter/oci/export.go)
rejects it. Smoke uses `load: true`, `type=docker`, and `provenance: false`;
standalone assembly explicitly passes `--load`. Clean proofs export
single-platform OCI directories with provenance disabled. Rehearsal's
`mode=max` provenance export writes an explicit `tar=true` OCI archive;
production uses the index-capable image exporter to push by digest.
Version, epoch, platform and digest-bound runtime inputs stay identical across
these exports. Provenance envelopes may create an index, so evidence walks
the layout and binds the actual platform manifest/config rather than its root
index. BuildKit's smoke `imageid` supplies the config digest for the proof;
the daemon-resolved image ID supplies the runnable smoke identity.

Alpine's APK repository and unpinned transitive dependencies still resolve
during a **cold runtime preparation**. A pinned Alpine index alone does not
freeze them. The exported OCI closure digest freezes all their bytes before
assembly, smoke, both proof runs and provenance export. Changing that digest
changes the input: equality across independently regenerated cold closures is
not claimed. Retain the complete runtime OCI layout when replaying old
evidence; the input/base hashes in JSON identify it but cannot recover its
bytes. Pin/package changes require the existing render-parity and NOTICE
review; unavailable pinned APKs fail preparation rather than being upgraded.

`scripts/reproduce-image.mjs RUNTIME_LAYOUT EVIDENCE.json` requires a clean
tracked checkout. It archives the exact commit into two distinct fresh source
directories, copies the exact already-generated history fixture, and builds
web and Go independently. Each run compiles Go in its own empty build cache,
so run 2 is a second compilation, not a replay of run 1 or of the smoked build.
The proof's exact scope: **same runner, same frozen runtime closure, same
history fixture**; the runs also share the `go.sum`-verified module downloads
and the lockfile-verified npm cache. It does not claim equality across
runners, toolchain installations or independently prepared runtime closures.
Neither compiled
outputs nor incremental web state are carried between runs. Each assembly
uses `--no-cache --network=none`, the same runtime digest, reserved version,
source epoch and normalization flags. It compares **platform runtime manifest
digests**, excluding provenance envelopes, and requires each rebuilt input
manifest to equal the original smoked build's inputs. It separately compares
the rebuilt config to BuildKit's smoked config digest; the daemon's runnable
ID is recorded independently because it need not equal that config digest.
Temporary fixtures remain under ignored `tmp/image-repro-*` for diagnosis.

Both workflows upload `image-assembly-<arch>-<run_id>-<attempt>` for 14 days:

- `inputs.json` (`aeon.image-inputs.v1`): tree, reserved version, platform,
  source epoch, generated-history/NOTICE/recipe/web/binary hashes and exact
  Go/Node/npm versions.
- `image-reproducibility.json` (`aeon.image-reproducibility.v1`): source SHA,
  those inputs, both runs' runtime manifest/config digests and build-cache
  paths, smoke IDs, `proof_scope`, `reproducible`, and failure/completeness
  fields. `runtime_base` identifies the runtime closure this build prepared:
  platform, manifest and config digests, layer diff IDs and the recipe hash.
- `image-timing.json` (`aeon.image-timing.v1`): timezone-bearing start/end,
  run/attempt/SHA/platform, assembly/smoke outcomes, `assembly_smoke_s`,
  `within_target`, and `completeness: {state, reasons}`. Missing/failed/skipped
  evidence is **unknown**, never zero or success, following
  `scripts/release-timing.mjs`'s validated interval conventions. After saving
  the JSON and step summary, missing, malformed, reversed or incomplete
  evidence makes the timing command exit non-zero. Start files are bounded
  to 128 bytes; oversized or non-regular files are treated as missing evidence.
  Both workflows run this command with `if: always()` and pass the assembly
  and smoke outcomes; failed or skipped work stays partial and fails the step.
- Runtime `index.json`: the frozen local OCI root descriptor. Archive the
  full runtime layout separately if a durable replay fixture is required.

The timing interval starts immediately before COPY assembly and stops after
the complete immutable-ID smoke, including image load, ID resolution, fixture
setup and cleanup. Complete successful samples over 90 seconds record
`within_target: false`, emit a GitHub `::warning::` annotation and a step-summary
line, retain their artifact and exit zero. The 90-second target measures
AEON-422 acceptance; it is not a release safety gate. Host
compilation, runtime preparation, setup, clean rebuild proofs and provenance
export are outside that named acceptance metric; their separate named Actions
step durations must still be reported when assessing total release latency.
Record native runner, cold/warm cache state and run attempt with every sample.

Hosted evidence remains pending until the coordinator integrates this change
and dispatches `release-image-check.yml` on the exact `main` SHA using the
existing pre-tag procedure. Download both architecture artifacts, require
`reproducible: true`, equal `runs[0].image.digest` and `runs[1].image.digest`,
matching inputs/base, complete timing, and full smoke/job success. Assess
`assembly_smoke_s <= 90` separately for AEON-422 acceptance; a slower measured
sample warns without blocking the release. Use `gh run view RUN_ID --json jobs,createdAt,updatedAt`
to retain per-step durations and cold/warm logs; compare like-for-like samples
to release 123's 142 s amd64 and 129 s arm64 build-plus-smoke baseline. Those
baseline numbers are OPS-provided, not measured by this worker. No local or
hosted runtime result is claimed by the fixture tests. The final tag build
must repeat these gates and retain its existing signer workflow and exact-SHA
receipt checks; rehearsing never grants publication or deployment permission.

Baseline evidence: [release run 36647379702](https://github.com/inspr-at/paimos/actions/runs/36647379702), obtained with `gh run view --json jobs,createdAt,updatedAt`, took **641 s (10:41)** from run creation to pushed digest. The release job began after **237 s**; Linux/CLI builds took **99 s**, artifact download **1 s**, history **32 s**, image smoke (including its original build) **119 s**, and build/push **122 s**. Removing the signing dependency and client build/download time gives a conservative structural estimate of **304 s (5:04)** before the new attestation/verification overhead, with cache gains unmeasured. The ≤6 min target and successful test-image verification remain pending a hosted run; do not report this estimate as measured acceptance.

#### Cold Go compilation for shipped binaries

No job that produces a shipped binary restores a Go cache. `actions/setup-go`
runs with `cache: false` in every job of `release.yml` and
`release-image-check.yml`. The reason: the go command never re-verifies a
build-cache entry, and the caches `setup-go` restores are also written by CI
jobs, including main-push shards on the self-hosted mbp2606 pool. A poisoned
entry would be compiled into a release, outside what
`--deny-self-hosted-runners` on the attestation covers.

Each compilation gets its own **empty `GOCACHE` under `RUNNER_TEMP`**: the image
binary, the release-history generator (its output is linked into the binary),
each of the two proof runs, darwin `paimos-agentd`, Linux `paimos-agentd` and
`aeon-cli`. `GOMODCACHE` is a new directory under `RUNNER_TEMP` per job;
`go mod download` fills it (every module is checked against `go.sum`),
`go mod verify` re-hashes every module zip and extracted tree, and builds run
with `GOFLAGS=-mod=readonly`. `scripts/build-image-inputs.mjs` and
`scripts/build-release-binaries.sh` enforce this themselves in GitHub Actions:
a missing cache variable, a path outside `RUNNER_TEMP` or a non-empty build
cache stops the build before any Go command runs. Local builds keep their
caches. The rehearsal uses the same settings step for step, and workflow tests
reject any drift.

The npm cache is still restored. It holds downloaded tarballs only,
`web/package-lock.json` pins a sha512 for every one of them (a prebuild test
requires that) and `npm ci` checks the hash of cached and downloaded bytes
alike, so altered cache content fails the install.

The cold compilations, like the cold runtime preparation, lie outside the
90-second assembly and smoke metric, whose boundary is unchanged. They do
lengthen the image jobs: each runs three cold Go compilations (the smoked build
and both proof runs) and one cold runtime preparation, which is why their
timeout is 30 instead of 20 minutes. Report their step durations when assessing
release latency.

#### Runtime closure: prepared cold on every build

There is no runtime closure cache. Every tag build, and every rehearsal,
prepares the closure cold: `scripts/Dockerfile.runtime` is built with
`no-cache: true` and without a registry cache, from the digest-pinned Alpine
3.24 index and the pinned `chromium` and `tini` packages. Nothing is restored
from an earlier build and nothing is saved for a later one. Workflow tests
reject a cache for it, a second source, a second preparation, and any step
after the platform job's digest handoff.

The APK repositories are resolved **once per build**, in that preparation
step. Its result is bound by digest at once: assembly, smoke, both proof runs,
the provenance export and the push use that one closure, and none of them
fetches runtime bytes again. The unpinned transitive packages are therefore
whatever Alpine 3.24 published when the build ran. Two releases can ship
different runtime closures without any change in this repository, and a
rehearsal's closure is not the release's closure: the tag build prepares its
own, then smokes and proves that one.

The runtime digests recorded per release are the traceability record. They
describe the closure that this build prepared and shipped, never a shared or
reused one:

- each platform job's summary: runtime manifest digest, config digest, layer
  diff IDs and the recipe hash;
- `runtime_base` in `image-reproducibility.json`: the same values;
- the draft release notes: one `Runtime closure linux/<arch>:` line per
  architecture with the runtime manifest digest.

Equal digests prove equal runtime bytes. Unequal digests are expected whenever
Alpine's repositories moved between two builds; equality across independently
prepared closures is not claimed. The digests identify the bytes but cannot
recover them.

#### Pushed image equals smoked image

Smoke and proof examine local builds; the push is a further export of the same
inputs. `scripts/verify-pushed-image.mjs registry` closes that gap in the tag
build: it reads the pushed digest with `docker buildx imagetools inspect --raw`,
requires the bytes to hash to the digest they were requested by, requires an
index holding exactly one `linux/<arch>` manifest besides attestations, and
compares that manifest's config digest with `steps.build.outputs.imageid`, the
config digest of the smoked build. The rehearsal runs the same walk over its
local provenance export (`layout`) and, read-only, over the digest-pinned
Alpine index named in the runtime recipe (`probe`), which exercises the real
registry reads without a push.

#### What only a tag build can prove

The rehearsal has no registry write, so these run for the first time in the
first tag build after this change (release 124):

- `Require pushed image to equal the smoked build` against a digest pushed to
  GHCR. Its logic and its registry reads are rehearsed; the pushed object is not.
- The runtime digests in the draft release notes, which travel through job
  outputs of the matrix job.

### Release timing (AEON-416)

`node scripts/release-timing.mjs` prints a compact table and JSON for four intervals: gate-ok → live, cut → live, PR → merge, and rollback. It reads GitHub with paginated `gh api --method GET` and `gh pr view`, plus optional rollout JSON (`--rollout`, one file or a directory of `aeon.rollout.v1` objects). `--method`/`-X` other than GET, `--field`/`-f`/`-F`, and `--input` are refused before `gh` runs. `--since` keeps releases whose start is at or after that timestamp and requires a timezone. `--release` matches the label (`12d`), version, sequence, or `stable110`. `--json` prints JSON only. `--fixture` replays a recorded bundle and does not call `gh`.

```sh
node scripts/release-timing.mjs --fixture scripts/testdata/release-timing/section1.json
node scripts/release-timing.mjs --since 2026-09-29T17:00:00Z --release 12d
```

Cut and live for the §1 baseline come from rollout `cut_at` / `live_at` (the release progress logs). Wall minutes are the truncated UTC-minute span, which is how that section counts 128, 38, 84 and 58. Every timestamp needs an explicit zone; a reversed interval is unknown. PR → merge, the digest step, PR CI and the pin PR come from GitHub. CI counts pull-request attempts that start at or after the release PR opens and finish at or before it merges. A rerun is timed from that attempt; without attempt history the duration is unknown. Gate-ok → live is the median only when every rollout ticket has a time, otherwise it stays empty. It stays empty for the §1 releases: there is no `gate/cross-family` status, and the historical verdicts are untimed ticket comments, so the published 3-5 h median is not recomputed. Rollback is the latest rollout interval, otherwise the latest pin PR whose title says rollback. A forward pin stays the forward pin. If those two durations disagree, or more than one forward pin matches, the value is omitted as ambiguous. Rollback stays empty for the §1 releases: none of them rolled back. The published ≈15 min is an estimate of a rollback pin PR. Measured forward pin PRs merged in about 11 min, and the 12d Pharos check is 9 min 19 s. The plan's 9.4 min is the progress-log span of that same check. Lists paginate until they are exhausted. If a list hits the cap while more results exist, `collection.truncated` names it and the report does not treat the missing tail as absence.

Quote the JSON as release evidence. This repository has no `RUNBOOK.md`; the process runbook is the PPM entry `runbook/flywheel`, which is not edited here.

### Native agentd qualification (AEON-487)

Before publishing the release or merging its tap PR, download the signed
`paimos-agentd-darwin-<arch>` asset and `SHA256SUMS` from the exact draft tag
with the coordinator's approved identity. Verify its SHA-256 against that file
and require `spctl --assess --type execute --verbose <asset>` to accept it.
On the operator's Mac, with the existing daemon stopped, run
`<asset> serve --setup-root <the operator's paired root>` in the foreground
for 10 seconds. Require the control socket (`daemon/agentd.sock` under that
root) to appear and the process to stay running; then stop it with Ctrl-C.
Run the attach preview before any Touch ID qualification. Record the exact
asset, digest and results; in-memory ACL fixtures alone do not qualify a release.
The native ACL fixtures must also pass with the affected five-entry shape,
exact root PROCESS selectors and the sole `teamid:P66J39QV6V` partition.
Unreadable legacy subjects or unexpected partition payloads fail closed;
qualification must use the existing paired item without rewriting its ACL.

### Qualify, publish agent assets, merge tap, then switch server (AEON-493)

Run this explicit step from the release coordinator's checked-out release commit after the native agent qualification step (AEON-487) has passed for the exact signed/notarized bytes, including the hardware and ACL evidence required by [Agent integration](AGENT_INTEGRATION.md#signed-release-daemon-aeon-285). Record the qualification evidence and verified image digest in the draft notes. Confirm the tag, draft state and complete nine-asset set (eight binaries plus `SHA256SUMS`); a crossbuild or fake service fixture is insufficient. Do not publish a failed or partial tag workflow. Publication is distribution evidence; the release notes must still say the server has not switched until live verification passes.

```sh
tag="v$(node -p 'require("./version.json").version')"
gh release view "$tag" --repo inspr-at/paimos --json tagName,isDraft,assets,body
# After native qualification and artifact inspection above, before the server switch:
gh release edit "$tag" --repo inspr-at/paimos --draft=false
gh release view "$tag" --repo inspr-at/paimos --json tagName,isDraft,publishedAt,url
```

Use the coordinator's approved GitHub CLI identity (or an approved GitHub App identity) with release write access. Enable [immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases) for future releases before publishing; GitHub locks their assets and associated tag at publication. Existing published releases stay unchanged. Verify the public API reports `immutable: true` and dispatch the trusted workflow from main after qualification/publication:

```sh
gh workflow run homebrew-tap.yml --repo inspr-at/paimos --ref main -f version="${tag#v}"
```

The workflow must already be independently reviewed on main. Its checkout is the exact main workflow commit; it never executes code from the release tag. The main-only required-reviewer environment is the credential boundary. A tag-shaped version or a workflow `if` cannot establish that boundary.

Confirm `isDraft: false`, public asset availability and the Homebrew workflow result/PR. The coordinator must finish the tap PR’s own checks and approval, merge it, and verify that Homebrew resolves the exact release and checksums before switching the server. If the tap job fails, fix its cause and rerun that job; do not rerun the tag build, toggle publication to retrigger it, replace assets, or reuse the coordinate. The tap PR still follows its own checks and merge approval. Missing app secrets mean no automatic PR; record that result for the coordinator to resolve before claiming Homebrew is updated.

Drafts are excluded from public release discovery and GitHub's `latest` endpoint. The tap script uses unauthenticated exact-tag metadata and asset requests, with an explicit published-state check before any tap mutation. The server guide keeps the direct download pinned to its running version and always offers Homebrew, which installs the latest INSPR release. Neither guide rendering nor startup contacts GitHub. Agents negotiate compatibility with their own server; matching release versions are unnecessary inside the declared protocol/minimum window.

This deliberately supersedes AEON-356's “publish after live verification” order: waiting until after the server switch made the new server's pinned agent downloads unavailable and left Homebrew on the previous release. Native qualification and immutable artifact/provenance checks gate distribution instead; the agent compatibility window covers a newer agent meeting the old server during the rollout. Keep AEON-356's draft assembly, immutable assets, exact checksums, human publication approval and failure handling. If server live verification subsequently fails, keep the old server or roll it back by exact digest; already published agent assets and tags stay immutable, and a fix needs a new coordinate.

The owning coordinator must move the external `~/Code/aeon-worktrees/deploy.sh` publish/tap block to immediately after native agent qualification, then wait for tap merge and verify exact public distribution before the server-switch block. Keep the existing server backup, approval, switch, health/version/digest verification and rollback gates. This repository does not own that script; AEON-493's draft PR records the required change for its owner.

### Automated live verification and distribution finalization (AEON-414)

`.github/workflows/verify-live.yml` is a coordinator-dispatched, hosted workflow
that runs only from `inspr-at/paimos` main. It defaults to verification without
release or tap writes. The coordinator supplies a trusted `aeon.rollout.v1`
record from the approved pin/host rollout; this extends the timing record rather
than introducing another rollout controller. The workflow has no SSH, deploy,
pin-writing or PPM credential. The worker's draft PR never runs this workflow
against production.

Before adding any App secrets, the lead must configure **both**
`live-verification` and `homebrew-tap` with the payload in
`.github/live-verification-environment.json` and exactly one deployment policy
`{"name":"main","type":"branch"}`. This selects only main and requires
Markus's approval (immutable user ID 276789). Operator dispatch and attended
operator approval are permitted; the JSON explicitly keeps self-review enabled.
Replace the old `v*` tap policy and remove any extra branch/tag patterns.
Store these credentials only as environment secrets, never repository or org
secrets accessible to arbitrary workflows. Protect the environments **before**
storing credentials; if credentials already exist, restrict their environment
before enabling either workflow. A modified branch workflow can remove its own
guards, so script checks cannot substitute for server-side policy.

`scripts/verify-live-policy.mjs` reads the actual environment and complete branch
policy list with a read-only Actions token. Missing reviews, extra patterns,
tags, API failures and partial lists refuse execution. The live workflow runs
this preflight in a job without App secrets before requesting the protected job;
both scripts recheck policy before minting tokens. Dispatch and reruns allow only
`markus-barta`/276789 on the exact main workflow ref. The supplied restart/5xx and
native qualification fields remain statements from that approved coordinator,
not cryptographically authenticated host measurements. The attended approval
must bind them to the collector/native evidence; do not accept arbitrary JSON
as native qualification.

Store approved non-admin
App identities as `RELEASE_APP_ID` / `RELEASE_APP_KEY` (paimos release contents
write) and `HOMEBREW_TAP_APP_ID` / `HOMEBREW_TAP_APP_KEY` (tap contents and pull
requests write). The release App must read attestations; public nixcfg pin
metadata is read unauthenticated through the API. The tap App also needs checks
and commit-status read permissions. Tokens are minted for one repository each
with exact requested permission maps, including GitHub's mandatory read-only
metadata permission; any missing, extra or upgraded grant is refused and revoked.
Attestation gets only attestations read in a new empty HOME/config directory and
its token is revoked before the live probe. Public source/assets use no App token.
Contents write is minted only inside the release-body PATCH and immediately
revoked. Read-only tap inspection uses only contents/PR/checks/status reads;
tap write/merge tokens exist only after verification and are revoked on success
or failure. Neither the Actions token nor an admin merge bypass performs writes.
The payload and preflight are repository code, **not evidence of installed
protection**. The coordinator must install/verify the policy, immutable-release
setting and App scope before adding keys, and record denial of a modified-branch
dispatch. No environment, repository setting, App installation, permission or
foreign rollout collector is provisioned by this worker change.

The dispatch input `rollout` contains JSON with these fields; obtain identifiers,
hashes and measurements from the trusted collector and qualification evidence,
never from the live candidate in the same verification step:

```json
{
  "schema": "aeon.rollout.v1",
  "direction": "forward",
  "outcome": "success",
  "version": "261001130110.0.0",
  "version_scheme": "inspr-calver-3",
  "image_digest": "sha256:<64 lowercase hex>",
  "running_digest": "sha256:<same OCI index digest observed by the host>",
  "source_commit": "<40 lowercase hex>",
  "pin": {
    "repository": "markus-barta/nixcfg",
    "number": 890,
    "merge_commit_sha": "<40 lowercase hex>",
    "merged_at": "2026-10-01T13:05:00Z"
  },
  "live_at": "2026-10-01T13:06:00Z",
  "observation": {
    "started_at": "2026-10-01T13:06:00Z",
    "ended_at": "2026-10-01T13:07:00Z",
    "restart_count": 0,
    "requests_5xx": 0,
    "requests_total": 100
  },
  "web": {
    "entrypoint": "/assets/index-<fingerprint>.js",
    "sha256": "<64 lowercase hex from the qualified image bundle>"
  },
  "qualification": {
    "version": "261001130110.0.0",
    "asset": "paimos-agentd-darwin-arm64",
    "sha256": "<64 lowercase hex>",
    "sha256sums": "<SHA256SUMS file SHA-256, 64 lowercase hex>",
    "operator": "markus-barta",
    "spctl": true,
    "foreground_socket": true,
    "acl_fixture": true,
    "attach_preview": true,
    "touch_id": true,
    "evidence": "AEON-487/comment/native-qualification"
  }
}
```

This is a shape example, not valid release evidence. The collector must report
the deployed **index** digest, not its architecture's child digest, and measure
container restart count and actual request counters over at least 60 seconds
after the switch. The observation must be no more than 15 minutes old, remain
fresh through verification, and contain nonzero traffic with zero 5xx and zero
restarts. Missing measurements fail closed. The owning nixcfg/operator tooling
must supply these fields and dispatch after pin merge; its integration and
first attended live run remain coordinator acceptance, not worker evidence.

`scripts/verify-live.mjs` rechecks the actual merged main pin PR and its single
image-line diff in `hosts/csb1/docker/compose-spec.nix`, annotated release tag,
source commit on main, tagged version/scheme and the hosted image attestation.
It requires the exact nine-asset release, qualified checksum-file hash and
downloaded SHA-256 of every binary. It polls `/api/version` for at most ten
minutes, then observes version, health including database, readiness, SPA
entrypoint and exact bundle hash for at least one minute. The isolated
Playwright smoke renders sign-in, checks the same bundle and version, and
blocks writes, login, third-party traffic, WebSockets and service workers. It
uses no operator profile or credentials. Probe 5xx counts and timestamps are
separate from the host's traffic counters; a failed probe stops the gate.

With `apply: true`, only after all gates pass, the script appends one bound
`Live verification:` line to an already published **immutable** release. Draft
publication is disabled, including recovery mode: GitHub's documented
[release PATCH](https://docs.github.com/en/rest/releases/releases#update-a-release)
has no atomic verified asset-id/digest precondition. A fresh GET, an `If-Match`
header or a post-publication recheck cannot prove an asset-set lock for a draft.
The script never sends `draft: false` and refuses drafts or mutable releases
before minting any App token. Server-side immutability protects the verified
tag/bytes; finalization compares the fresh stable asset ID/name/size/digest set
to the verified set and requires an ETag, sent as `If-Match` for additional
metadata defence. It does not claim that header implements an asset-set CAS.
Identical reruns leave the line and assets unchanged. **Keep AEON-493's order:**
qualified agent publication and tap merge precede the server switch; post-switch
verification finalizes that immutable release. Native qualification and human
publication approval remain required.

After publication, public `SHA256SUMS` must equal the qualified file. The script
reuses `homebrew-tap-pr.mjs`, refuses a downgrade or conflicting existing branch,
and verifies the sole formula change against the exact darwin checksums. Public
checksum downloads are pinned to the API asset ID and SHA-256, bounded by the
asset size, and follow only GitHub's HTTPS release-asset host without forwarding
authorization. The formula writer receives the original qualified bytes and
does not download a second body. A retry may update an existing branch only at
the exact default-branch tip. The formula commit has that observed parent and
updates only the work branch with `force: false`; a concurrent advance refuses
before opening a PR. Unrelated branches are preserved. It
waits up to five minutes for the tap PR and green checks, then merges at the
verified head through the non-admin API, subject to the tap's review and branch
policy, and rereads the installed formula. Changed heads, extra files, failed,
absent or incomplete checks fail closed. Already installed exact formula bytes
are a no-op. Failed checks stop finalization and tap writes; drafts are always
untouched. Failures after finalization retain the immutable public assets and stop further
actions. Rerun verification with the same evidence after fixing the cause; never
unpublish, replace assets or reuse a coordinate.

Every run writes a redacted `live-verification` artifact and job summary; a
failure emits an Actions error for the lead. Native hardware/Touch ID
qualification and its evidence remain manual. Under accepted decision **D7 B**,
the lead still announces through `aeon tell` and closes tickets; this workflow
does not inject a PPM key or send an inbox message. Deployment/rollback and
environment bootstrap remain separate human/coordinator controls.

Regression coverage: `node --test scripts/verify-live.test.mjs scripts/verify-live-policy.test.mjs scripts/homebrew-tap-pr.test.mjs` exercises refusal,
idempotence, forbidden draft publication, exact token scope/lifetimes, installed
policy validation, source binding, asset/redirect/branch races and
secret-safe output. `web/e2e/live-verification.spec.ts` exercises the actual
browser driver against isolated HTTP fixtures in PR CI. Neither is a claim of
production acceptance or native hardware qualification.

## Image smoke gate

`scripts/smoke-image.sh` exercises the release image before anything is published. CI sets `AEON_SMOKE_IMAGE` to the cached assembly's immutable image ID, which skips rebuilding and preserves the caller-owned image during cleanup; standalone runs use `scripts/assemble-image.mjs` to compile externally, prepare the runtime and load one disposable image, then clean up that image. Standalone assembly needs Go, Node/npm and a Buildx `docker-container` builder (OCI export and named contexts); it generates offline history only when no generated fixture exists. It checks the pinned Chromium and tini packages, their licenses, and `NOTICE`. It then starts a disposable Postgres and the server with mounted secret files, and checks startup, the database role, UID/GID 65532, health, and headers. The script's dev mode is only for authenticated upload and quote calls. Live OIDC is not part of the gate, because the database is disposable and has no identity provider.

The gate needs Docker. It is a release check, not the day-to-day `just test` run.

## Release assets

| Asset | Where |
| --- | --- |
| Server image | `ghcr.io/inspr-at/aeon:<version>`, one linux/amd64 + linux/arm64 OCI index, provenance and GitHub attestation per platform and index |
| `aeon-cli-darwin-arm64`, `aeon-cli-darwin-amd64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64` | GitHub release for the `v` tag. Install the file as `aeon`; a symlink named `paimos` selects paimos mode. |
| `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, `paimos-agentd-linux-amd64` | Same GitHub release. Darwin binaries link LocalAuthentication. Linux binaries are static. The current Nix package is named `aeon-agentd` and builds `bin/aeon-agentd`. Both names come from `cmd/aeon-agentd`. |
| `SHA256SUMS` | Same GitHub release, covering the CLI and agentd files above. Check it with `sha256sum -c` or `shasum -a 256 -c` before installing. |
| Flake | `flake.nix` in this repository. `packages.<system>.aeon` is the CLI plus a `paimos` symlink. `packages.<system>.aeon-agentd` is the supervisor. The version is the `version` field of `version.json`. |

Nix install of the CLI:

```
nix profile install github:inspr-at/aeon#aeon
```

The flake reference keeps resolving after the repository is renamed to `inspr-at/paimos`, because GitHub redirects the old name.

The four binaries are cross-built, not a claim that all user-service lifecycles work. `.github/workflows/pairing-platform.yml` runs an isolated fake-executable launchd or systemd-user fixture on `macos-15` (arm64), `macos-15-intel` (amd64), `ubuntu-24.04-arm` (arm64), and `ubuntu-24.04` (amd64). A platform is qualified only after that runner's real service install, status, drain, and removal check passes on the integrated commit. Other macOS releases and Linux distributions have no lifecycle evidence from this matrix.

Published coordinates are immutable. The existing stable86 release `v260927181849.0.0` predates the fourth daemon target: its `paimos-agentd-darwin-arm64` asset answered HTTP 200 and its `paimos-agentd-linux-arm64` asset answered HTTP 404 in read-only HEAD checks on 2026-09-27. A guide serving that version must omit Linux arm64 rather than point at a future asset or rewrite stable86.

Screenshot data for a dev tenant is `aeon demo seed`. See [DEMO.md](DEMO.md). That command is not part of the release tag workflow.

## Ticket benefits and release-note snapshots (AEON-256)

Tickets store `pill_en`, `pill_de`, `benefit_en`, `benefit_de` and
`hide_from_release_notes` in `nodes.fields`. Migration `0896` adds their optional
schema properties for every tenant and replaces `aeon_seed_node_kinds` for new
tenants. It changes no node values, translations, events or release artifacts.
Custom unrelated properties and constraints stay in place. Schema requirements
are deliberately optional so incomplete drafts can be created with warnings.

The generic node API requires both pills (2–4 whitespace-separated words) and
both nonblank benefits when a ticket enters a built-in completed state (`done`,
`accepted` or `delivered`) from outside that set, including creation in any of
those states, direct PATCH, bulk changes, CLI calls and bulk undo. Normal updates
check the final fields and state while holding the node row lock. Bulk skips an
incomplete ticket with a reason; bulk undo rejects the entire invalid reversal.
An already-completed ticket remains editable, including transitions within that
set, without fabricated backfills; reopening and completing it again invokes the
requirement. Hiding a ticket is not an exception. Sentence count, positive plain
language and translation fidelity are
editorial requirements, not claimed as machine-verified. This is an application
transition rule, not a SQL constraint: historical import/migration writers retain
their existing behavior. Cancellation, archival and tenant-defined state names
are not guessed to mean successful completion.

For a new release, read the supported authenticated endpoint
`GET /api/projects/{projectId}/releases/{releaseId}/note-snapshot` using a
project-scoped `releases.read` and `nodes.read` key (or an authorized person). The tenant comes
from authentication, not an input parameter. The export uses one SQL statement:
`journey_tickets.release_node_id` is the sole membership source, joined by tenant
and project; `nodes.fields` supplies exactly the five benefit properties.
Backlog tickets, Git mentions and a project's other releases are not membership.
Deleted/unavailable public members remain explicit gap entries. `captured_at`,
release revision and each member's `updated_at` record the observation. No live API or
classic database is contacted by the history builder.

At reservation, the release coordinator reviews that export and freezes its
public projection in `internal/releasehistory/data/product-notes.json` **before
the release PR** (AEON-405), so the release ships its own notes. Publication
only verifies that capture; it does not fetch notes for the following release:

```sh
go run ./internal/releasehistory/packnotes -repo . -snapshot SNAPSHOT.json -reserve VERSION -tenant TENANT_UUID -project AEON_PROJECT_UUID
```

`VERSION` must match `version.json`. Both UUIDs bind the source to the selected
PPM tenant and AEON project. This explicit reserve step may freeze an unpublished
preview; historical snapshot imports must already be frozen. Historic ticket
exports use the separate explicit workflow below. Use the configured API
client with `releases.read`/`nodes.read` to obtain the export. Keep the raw export
in its authorized local context: it may contain hidden text and tenant IDs and
must not be committed as the public projection. The generated file contains
only public ticket keys, pills, benefits, captured groups and capture provenance.
Review and commit it with the reservation. Identical reruns are idempotent
only with that same export file: a fresh export has a new `captured_at` and
conflicts. Restore the file before re-reserving; do not export the preview again.
Conflicting entries fail instead of rewriting a reserved version.

Every release also has a codename (AEON-430): an alliterative science-fiction
name such as "Cool Chip" or "Solar Star", a pure function of
`release_sequence` from the frozen, append-only lists in
`internal/releasehistory/codename/words.txt`. The letter steps through a
fixed cycle of the 15 letters rich enough for thousands of good names
(A B C D E F G H I L M P R S T), so release 1 is A and neighbouring releases
start differently. Within a letter short names come first, and no name
repeats before sequence 43,913 (50,419 names in version 1). The reserve step above writes it into `version.json` as
`"codename"`, right after `release_sequence`; without a snapshot, run
`just release-codename` (`go run ./internal/releasehistory/codename/stamp -repo .`)
once `release_sequence` is set. Both are idempotent and refuse a name that
differs from the sequence's. The codename is presentation only: the version
stays the identity, and every earlier release has its name from the same
function. To change the lists, append a new `version N from S` block with `S`
above every reserved sequence; never edit, reorder or delete a line, so no
existing name moves. `codename.Guard` enforces this in `just release-history` (and `TestGuard`, `TestRepositoryCodenames`): a recorded codename must stay its sequence's name, and a version after 1 must start above every release without one. Two-word collisions with obscure titles are an accepted
residual risk. A reported collision is added to the pair deny list in the next
list version; names of already-published releases never change.

For historical backfill where snapshots exist, export stored snapshots as `VERSION.json` in
one directory and run the same command with `-snapshots DIRECTORY -tenant
TENANT_UUID -project AEON_PROJECT_UUID`. A run with no exports imports only
authoritative snapshots already in local tags and reports missing versions.
Alternatively, save an authorized PPM `GET /api/releases` response, record that
workspace's `tenant_id` and `project_node_id` on the saved file, and pass
`-history HISTORY.json -tenant TENANT_UUID -project AEON_PROJECT_UUID`. Prefer
`-history` over `-snapshots` for backfill. A snapshot file from before group
storage has no group, and this snapshot import cannot derive one offline; those embedded
items then take a group from the viewing tenant's own tickets, which usually
means Features. `-history` records the group the serving workspace already
derived. The two identifiers must match the flags; a file for another workspace
is rejected. This consumes only `database-snapshot` or immutable tag-snapshot
notes, never `changes.linked_tickets`. It records each ticket's group, including
a group the server derived from the live classification when the snapshot itself
had none. The explicit historic-ticket workflow below is the only import of current
ticket fields for missing snapshots; release PR bodies and pills.tsv are not sources.
Existing tags and artifacts stay unchanged; the new
binary carries the backfill. Migration `0997` captures future groups alongside
the five note fields. Older snapshots have no group. Serving those classifies
every commit ticket from the current classification, not only the tickets the
capture tells: a hidden bug, a ticket with no pill or benefit, and a commit
ticket that was not a release member. A bug is a fix and a visible benefit is a
feature, the same rule as AEON-289. Only those two facts are read. The captured
pill and benefit stay frozen, and live pill or benefit text never enters
`linked_tickets`. The history export above records that group, so other
workspaces see Fixes from the embedded notes.

A release with no capture uses that same classification for `changes[].group`.
It loses live-text Highlights by design: the no-live-text rule keeps pill and
benefit text out of the response, so Highlights has nothing to show until a
capture exists. Compare still follows the classified group.

A capture that already stores a group keeps that group and its frozen text.
Commit tickets the capture does not name — a hidden member, a member with no
pill or benefit, and a ticket that was not a release member — still take
`changes[].group` from those same two live facts. The note on that
classification stays empty, so live pill and benefit text never reaches
`linked_tickets` or Highlights. Tickets the capture already grouped are not
read again. A shared commit still takes the strongest group. Compare follows
that group.

For images built before their release tag, run
`go run ./internal/releasehistory/generate -repo . -repository inspr-at/paimos -offline -candidate VERSION`.
The version must match committed `HEAD:version.json`, including its scheme,
channel, sequence and reservation instant. The candidate uses HEAD's commit and
captured notes: a matching committed `release-notes/VERSION.json`, otherwise the
reviewed public projection in `product-notes.json`. Missing or mismatched captures
fail the build; uncommitted edits do not supply candidate identity or notes.
`GITHUB_RUN_ID`, `GITHUB_REPOSITORY`, `GITHUB_SERVER_URL` and `GITHUB_WORKFLOW`
identify the building workflow when available. Its status is recorded as in
progress, with no invented conclusion. Without those inputs CI evidence is pending.

The candidate is visible in the release history, named by its codename, with
Features/Fixes from its captured notes. Its tag, headline, tag/publication times,
image digest and release-run link stay empty or null; `evidence.pending` names
what remains to be established. A later tagged build fills those fields through
the normal history generator; it neither rewrites the original manifest nor
creates a second entry for the version. Existing published entries keep their
normal tag-based generation. Candidate mode does not publish, tag, or deploy.

`internal/releasehistory/generate` also reads legacy `release-notes/VERSION.json`
files **from their matching annotated Git tags**, including under `-offline`.
Later ticket edits cannot change those notes. The generated manifest records the exact
file SHA-256, tag/path, capture time, revision, both languages, hidden count and
gaps. Portable notes are the additive `notes.public_items`, with no tenant UUIDs;
the existing `notes.items` schema stays unchanged. Members sort by recorded position, then key and ID. Exact duplicate IDs
collapse; conflicting duplicates, duplicate keys, malformed metadata and a
recorded version that differs from the tag fail the build. If the release had no
assigned version at capture time (candidate registration can happen later), the
file's tagged path is the explicit coordinator-supplied build binding; that
missing recorded version stays a visible provenance gap. It is not inferred
from a ticket, timestamp or Git headline.

Missing snapshot files produce empty notes with a membership/field-data gap.
Incomplete visible tickets produce field-specific gaps; they get no invented
translation or Git-headline benefit. Hidden tickets contribute no text or key to
the public notes; they contribute only to the hidden count, even when benefit
fields are incomplete or the member was unavailable at capture.
Technical Git headlines, legacy top-level `tickets` references and changes remain
evidence; membership claims come only from the snapshot. The release detail shows
each captured ticket as one block under Features or Fixes (pill as heading, key,
benefit sentence, commits folded), exactly as it shows linked tickets of a release
without a snapshot; the tag message appears only under Evidence. Old v1 manifests without the optional
`notes` member and regenerated records with `notes.source = "unavailable"` keep
their ticket references, ticket filter and commits, but their tag message is not
shown as a title; their Git text is not presented as benefit notes or membership.
Available snapshots remain authoritative even when empty or incomplete. This
additive reader boundary does not modify any existing published artifact or
legacy tag.

Integration acceptance still belongs to the coordinator: review migration0896,
select and approve the production release/project mapping, capture/review/commit
an authorized snapshot before the next tag, verify its digest in the resulting
artifact, and live-test both languages. Automatic production snapshot capture,
server-side signed/sealed snapshots, GitHub release-body rendering, translation
backfills and company-rule publication are not implemented here. An omitted
snapshot is intentionally a visible gap, not a successful benefit-note release.
The writing-rule proposal is `docs/proposals/ticket-benefit-writing.json`, using
AR1's draft Rule DTO. It must be imported as a draft at the fetched revision and
published separately by an authorized human; it changes no effective harness
files or company rules.

### Own-release notes without journey membership (AEON-405)

At reservation, write an ignored JSON array of the release's reviewed ticket
keys (for example `tmp/release-scope.json`). Use the exact scope being cut,
including hidden members; `[]` explicitly declares an internal-only release.
Do not infer this scope from earlier tags or require the release to be published.
After updating `version.json`, capture through the configured PPM client and
freeze the public projection in the same release PR:

```sh
go run ./internal/releasehistory/exporthistoric -repo . -client /path/to/ppm-client -tenant TENANT_UUID -project AEON_PROJECT_UUID -reserve VERSION -tickets tmp/release-scope.json -out tmp/own-release-notes.json
go run ./internal/releasehistory/packnotes -repo . -historic tmp/own-release-notes.json -reserve VERSION -tenant TENANT_UUID -project AEON_PROJECT_UUID
node scripts/check-own-release-notes.mjs
```

The explicit version, channel and sequence must match `version.json`. The
exporter verifies the authenticated tenant and each member's project ancestry.
Every public member must have both 2–4-word pills and both nonblank benefits;
hidden members contribute no key or text. The raw ignored export stays local.
Identical reruns use that same export; changed observations conflict with the
frozen original. Commit the public bundle alongside the version reservation
before opening the PR. CI's `release-check` rejects a missing own-version entry,
a mismatched sequence/channel or invalid bilingual fields. AEON-405 pins the
legacy cutoff at release sequence **113** (`LEGACY_RELEASE_SEQUENCE_CUTOFF` in
the checker): captures through that sequence may omit channel/sequence and
bilingual fields, but every field present is still validated. From sequence
**114** onward, the complete matching channel/sequence and all four bilingual
fields are required, even when `written_after_release` is set. The cutoff stays
fixed; it does not follow `version.json` or future releases. Historic entries
remain immutable; reserve and capture the next release rather than rewriting a
legacy entry to satisfy the gate. After
publication, verify the same capture digest and both languages on the target
instance; do not recapture or rewrite it.

### Reviewed corrections and fix markers (AEON-405)

File bug-fix tickets with `paimos issue create ... --bug` (or `--tags bug`).
The MCP `issue_create` argument schema exposes the same `bug` and `tags`
fields; its existing R1 placeholder remains, so file through the CLI today.
The helper preserves the ticket kind and writes the literal `bug` tag that the
release classifier reads. Mark actual repairs when filing them; feature work
keeps its feature classification. A mixed ticket's benefit can still describe
its real new capability without overstating the repair.

Published frozen entries stay insert-only. Reviewed corrections live separately
in `internal/releasehistory/data/product-note-corrections.json`, with a version,
ticket key, original snapshot SHA-256, reason, and new group and/or existing
bilingual text. A correction must target a public item already in that capture;
it cannot add hidden or absent members. Invalid bindings fail the history build.
The served notes keep the original digest and expose the exact correction
records under `notes.corrections`; Details → Evidence shows their keys and
reasons. Tenant-local snapshots retain their existing precedence. The AEON-405
layer applies the reviewed fix/feature and wording audit to each frozen public
occurrence without rewriting the bundle, tags or published artifacts.

### Historic product notes without journey membership (AEON-398)

Historic AEON tickets often have no journey release assignment. For published
releases without a capture, use the same Git tag history and union of release
and commit ticket keys as the history builder. This is explicitly later
`release-manifest-tickets` evidence, not original journey membership. No
database write, migration, tag rewrite or deployment is involved.

The history also recognizes historical lightweight release tags when their
committed `version.json` matches the tag and the coordinate is not an unpublished
reservation. Their channel and sequence come from that file, and their release
time comes from the tagged commit's committer date. Keep published tags unchanged;
new releases require annotated tags as described above.

From a full checkout with release tags, use a configured PPM agent client (or
wrapper) with `nodes.read`. It must select the approved PPM tenant; no credential
is passed on the command line. Both source UUIDs are explicit:

```sh
go run ./internal/releasehistory/exporthistoric -repo . -client /path/to/ppm-client -tenant TENANT_UUID -project AEON_PROJECT_UUID -out tmp/historic-notes.json
# Review the ignored local export, then freeze only its public projection:
go run ./internal/releasehistory/packnotes -repo . -historic tmp/historic-notes.json -tenant TENANT_UUID -project AEON_PROJECT_UUID
```

The exporter verifies the authenticated tenant, resolves each node's kind and
project ancestry, and reads the four bilingual note fields, type, tags and hide
flag. The output must be ignored and inside this checkout; an existing file is
never overwritten. Keep it local: hidden text is present for the projection
check and must never be committed. API failures, missing tickets, wrong source
bindings, duplicate keys and invalid hide flags fail the import rather than
publishing a partial history. A later release can repeat these two commands with
a new export filename; already captured versions remain unchanged.

`packnotes -historic` reuses `ParseTicketMeta` and the live linked-note projection
for Features/Fixes, including bug kinds, types and tags. Hidden notes are omitted;
tickets with no note text stay under Other. Missing translations stay empty.
The public bundle contains only ticket keys, existing note text, groups and
capture provenance. Its digest covers the version, manifest membership source,
capture time and selected ticket observations; `written_after_release` is true.
Only published versions without captures are added; reservations and existing
snapshots are skipped. Review and commit `internal/releasehistory/data/product-notes.json`
with the code. Export counts distinguish releases and ticket occurrences across
releases (classified, Other and hidden); one ticket can occur in several releases.

Regression: `TestHistoricNotesNonPPMTenant` builds historic Git membership,
imports the export and serves it without tenant ticket data. The Playwright
`release-historic-tenant.spec.ts` consumes that HTTP output and exercises both
groups, the release filters and Highlights/Details at desktop and phone widths.
`GET /api/releases` and `GET /api/releases/{version}` support agent keys with
`releases.read`; presentation writes remain person-only.

## Historical note backfill (AEON-290)

After migration `0939`, the deployed binary supports an offline maintenance
command against its configured database (no Git checkout, API login or network
history lookup). Both dry-run and apply require an explicit **active person**
with workspace `roles.manage` authority. Agents and inactive people are denied;
there is no implicit operator. Tenant and project visibility come from the
person's live bindings. The command does not run migrations.

```sh
aeon release-notes backfill --tenant inspr --project AEON --actor-principal-id PERSON_UUID --all-missing
aeon release-notes backfill --tenant inspr --project AEON --actor-principal-id PERSON_UUID --release VERSION --apply
```

The container entry point is `/paimos release-notes backfill` with the same
flags. Omit `--apply` for a read-only plan. `--release` and `--all-missing` are
mutually exclusive; omitting both means all missing snapshots in that project.
The JSON report lists version, tickets found, notes count and hidden count for
each planned/inserted capture, plus `excluded_keys` (resolved manifest tickets
that are not completed) and `gap_keys` (captured public tickets with incomplete
fields or unavailable members), plus unchanged and skipped counts. Hidden
tickets never contribute gap keys.

For historical tags, membership is the union of ticket keys in the embedded
manifest's release and listed commits, resolved only within the selected tenant
and project. Only completed tickets (`done`, `accepted`, `delivered`) are
captured; other states are excluded and reported. This is an explicit
approximation from Git evidence, recorded as
`membership_source: release-manifest-tickets`; it is not original journey
membership. Unresolved keys are not treated as tickets. The current five benefit
fields and their update times are captured once, including hidden tickets for
provenance. Public notes contain only the hidden count, never their benefit text
or missing-field warnings; this is decided when reading the frozen snapshot.
`released_at` uses the manifest publication time, or its tag time when publication
time is absent; neither is replaced with the capture time. Captures without an
original release time are skipped. Reservations are never captured.

The version-keyed table is tenant/project protected and insert-only, with the
same immutability trigger as native snapshots. Each insert records the person,
`backfilled: true`, `label: backfilled`, capture time and one audit event. Apply
is atomic across both paths. Reruns leave existing rows unchanged. Native journey
releases still use their own membership and snapshot store, through the same
person/admin authorization and project/version selectors.

`GET /api/releases` and its detail route use stored journey snapshots first,
then explicit version-keyed backfills for the visible AEON project, then the
embedded tag/public product notes. Tenants without that project use the public
product notes too. Empty or hidden-only tenant captures remain authoritative.
Without any capture, the historical evidence remains. The overlay is computed per request and never
changes the shared embedded manifest. A malformed database snapshot is logged
without its payload and falls back for that release alone; other releases remain
available. Releases with no public notes or gaps show a quiet **Internal changes
only** in the detail (or **No notes for this release** without any commit); their
tag message is evidence only. Backfilled notes
show **Notes written after release**. No original tag, artifact, published
timestamp, or existing snapshot is rewritten. The coordinator owns production dry-run review and apply.

## Release presentation (AEON-305)

Every release introduces itself with a short **theme** (the kicker, 1–80
characters, one line), one **headline** sentence (up to 200 characters, one line)
and a short **intro** (two or three sentences, up to 600 characters), each in
English and German. The release detail shows them as a compact header above the
blocks every release shows: Features and Fixes with one block per ticket (the
pill as heading, the key, the benefit sentence and its commits folded), then
Other changes. A captured or backfilled snapshot decides which tickets are told
and their text. Without a capture, Aeon does not fill Highlights from live
ticket text: that is the no-live-text rule. Compare still uses the served change
group. A ticket is a fix when it is a bug (the served change group), else when
its commits are only `fix:`; otherwise a feature. The release list shows version,
date and theme, or the pills. A release without a presentation has no header.
The Git tag message is evidence, never a title; "Notes written after release"
is one muted line.

Presentations live in `release_presentations` (migration `0945`), keyed by
tenant, product project and calendar version, protected by tenant RLS and project
visibility, separate from tags, manifests and note snapshots. The version need not
be in the running build yet, so the presentation can be written before the new
build is live. English theme and headline are required; German fields may be
empty and readers then show English. Writing identical text changes nothing.
Every change records one `release.presentation_set` event (before and after);
removing one records `release.presentation_cleared`.

**The release agent must write the presentation for every new release**, in both
languages, as part of the release, right after the notes snapshot is final and
before announcing the release. It is written against the production database from
the deployed container, like the backfill, and is a dry run until `--apply`:

```sh
/paimos release-notes present --tenant inspr --project AEON --actor-principal-id PERSON_UUID \
  --release VERSION \
  --theme "Releases with a name" --theme-de "Releases mit Namen" \
  --headline "Every release says what it is about." --headline-de "Jedes Release sagt, worum es geht." \
  --intro "Two or three sentences." --intro-de "Zwei oder drei Sätze." \
  --apply
```

or with the six fields as a JSON file (`-` reads stdin; use `docker exec -i`),
which avoids shell quoting:

```sh
/paimos release-notes present --tenant inspr --project AEON --actor-principal-id PERSON_UUID \
  --release VERSION --file - --apply < presentation.json
# {"theme_en":"…","theme_de":"…","headline_en":"…","headline_de":"…","intro_en":"…","intro_de":"…"}
```

`--clear` removes a presentation. Outside the container the binary is `aeon`
with the same arguments. The actor is an active **person** with `releases.deploy`
on the project (workspace admins and owners); offline there is no agent key, so
the release agent names its operator, as for the backfill. The command prints a
JSON report with `applied`, `tenant_id` and `change` (`version`, `changed`,
`before`, `after`). People can also use `PUT` and `DELETE
/api/releases/{version}/presentation` (same authority; optional
`expected_revision` answers 409 when stale).

Write for the reader, not the repository: the theme names what the release is
about in a few words, the headline says what changes for them in one sentence, the
intro adds context in two or three. No ticket keys, package names or commit
jargon; those stay in the rows and the commits.

Status autopilot (AEON-521) is the final post-release check: release publication
through the journey calls the deterministic hook in the publication transaction.
Only linked tickets already Done at publication become Delivered; tickets awaiting
a human check get a skip comment. The hook applies at most 50 tickets, queues the
rest durably, and the worker drains committed batches every minute. A ticket
failure is isolated from release settlement; disappeared releases are skipped.
Verify their Activity reasons and `/api/status-autopilot/changes`; a flagged ticket
gets a comment and is retried after its check is cleared. Historical releases
are discovered once; queued work and the daily ticket cursor survive restarts.
Workspace admins configure the seven limits in Settings → Workspace → Autopilot,
with Inherit / On / Off per project. The daily UTC job lists triage and cancellation
suggestions, reminds blocked work, reopens stalled work and accepts deliveries
after the saved period (30 days by default). Any person comment after delivery
is conservatively treated as an objection. Automatic changes use the existing
`POST /api/events/{eventId}/undo`; later edits cause a conflict rather than
overwriting the ticket. Settings lists all current triage, cancellation, blocked
and missed-release flags through `/api/status-autopilot/changes?suggestions=true`,
independent of the latest 50 changes. Project Off runs no rules; workspace Off
keeps New/Backlog suggestions. Human checks pause only delivery and acceptance.
Progress inactivity uses actual harness/session-branch and review-PR timestamps,
so a title edit or comment does not keep stalled work in progress. Triage
autopilot's judgement modes are a separate phase.


### Work-node maintenance switch (AEON-648 / AEON-684)

Before stopping the release-122 container, run the candidate binary's
`paimos migrate --check` (or `aeon-server migrate --check`) with the existing
server database configuration. This command opens a read-only repeatable
snapshot, pages every tenant under FORCE RLS, prints each busy parent key and
reason using the same predicate as migration 1215, and applies no migrations.
Exit 78 means a live legacy work parent has a live direct work child and a
bound unstopped session, queued/starting/running/waiting claim, or running child
work order. Keep the old container running while claims drain and cooperative
handovers complete. Rerun the check before the switch; a clean snapshot is not
an execution permit and 1215 still rechecks under its maintenance locks.
Preflight errors or timeouts are failures, never clean results. Large reports
stream in bounded pages; the stderr summary explicitly truncates after 100.

Boot refuses the same blockers with one diagnostic naming their keys and the
remedy, exits 78, and performs no internal retry. OPS must not stop the old
container while this check fails. Existing schema-conflict, backup, human
approval, paused-writer and release gates remain authoritative; the worker's
preflight does not grant deployment approval.

OPS's 2026-10-04 csb1 real-data rehearsal (AEON-684) migrated 241 files from
schema 1148 through 1230 in 8 seconds. Epic 317 + task 811 + ticket 5239 became
6367 work nodes; keys, parents, titles, counters and relations were
byte-identical. These are OPS-provided facts, not a rehearsal performed by this
integration worker, and do not claim later migrations were rehearsed.
Release 122's server boots on that migrated database despite not knowing the
`work` kind. Boot success therefore does not prove rollback correctness.
**Rollback requires restoring the matching verified pre-switch database and
file/blob dump plus the exact old artifact; merely repinning release 122 is
unsupported.** A partial later migration failure can leave 1215 committed;
never delete ledger rows or modify published migration bytes to retry.

Integration retains ledger-owned 1237 for release-leaf lifecycle and renumbers
only the unpublished colliding parent-benefit generation file to reserved
1239. No published migration or release coordinate changes.

AEON-648 integration fix round 1 retains per-work residency floors in admission
and live routing, saved account/group pins, canonical Repeat actions/templates,
and upgraded imported issue links. Reserved forward migration
`1240_work_account_pins.sql` widens only the pin guard's target kinds and retains
its tenant, live-target and harness checks. It has a pinned policy exception
requiring consolidated coordinator review; historical SQL stays unchanged.
Regression and validation evidence is in
[aeon-648-int-fix1-evidence.json](qa/aeon-648-int-fix1-evidence.json).
The deletion regression again retains a real queued run, generated order and
capacity hold through a failed deletion. Migration 1238's retrospective
AEON-655 ledger entry remains a coordinator action. Linux browser CI and
OPS-247 remain unverified; local single-file Chromium evidence is not a
replacement release gate. This fix round neither pushes to origin nor deploys.

## Merge-friendly CI manifests (OPS-257 L13, stage 1)

Maintain `scripts/ci/{go,web}-test-tiers.json` and `web/ci-web-shards.json`
with `node scripts/test-tiers/cli.mjs manifests --write`; verify with
`node scripts/test-tiers/cli.mjs manifests --check`. The existing fixed
`go-static`/nightly script-test command checks canonical form; these root script
tests are outside the dynamically collected Go and `web/tests/` inventories.
No workflow command or static-check registry changes are needed.
`node scripts/test-tiers/prove-manifests.mjs [FULL_BASE_SHA]` independently
compares against the local pre-conversion commit (default: HEAD), allowing only
row-list order changes. It checks every tier, timing weight and metadata value,
retains duplicate multiplicity, and emits the base SHA and row counts as JSON.

Tests sort by `(kind, owner, name, occurrence)` using ordinal comparison: owner
is package for Go and file for every other kind, even if a row carries package
metadata. Occurrence preserves distinct native registrations with identical
titles; Go rows reject occurrence because Go identity is package plus name.
Post-gate identities, deletion candidate rows and other keyed row lists sort by
identity. Timing owner maps sort by owner, with one complete timing value per line.
Shard specs sort by file within their group; group order, flags, tiers and weights
stay intact. Row fields use `kind, package, file, name, occurrence` first and
ordinal key order afterwards, including nested objects. Exact duplicate rows can
be deduplicated by `--write`; differing values under one identity are errors.

Each row occupies one line, with JSON commas on separate lines. Inserting or
removing a row changes its line and one separator line, never a neighboring row,
even at a list boundary. Strict JSON cannot support a one-line edit at every
boundary without introducing a sentinel or changing the data model. Empty lists
retain separate opening and closing lines. Sorted edits spread additions through
the file; concurrent additions into the same gap can still conflict in GitHub's
text merge. GitHub's merge queue ignores local custom merge drivers.

Classify discovered, currently unclassified tests without hand-editing JSON:

```sh
node scripts/test-tiers/cli.mjs classify --tier GATED-FULL --kind go --only internal/auth:
node scripts/test-tiers/cli.mjs classify --tier NIGHTLY --kind web
```

`--kind` omitted covers both inventories. `--only` is a literal substring of
the existing discovery identity (package/file/name), not a regular expression.
Known rows retain their classifications; unmatched and stale rows retain the
existing runtime warning/default policy. Known-flaky ESSENTIAL restrictions still
apply. Legacy `classify go|web` keeps its NIGHTLY default and strict stale check,
and now writes canonical form. Web discovery lists cases without launching browsers.

Once per clone, install the self-contained driver at a stable absolute path
outside the repository, then configure local merge-main rounds. This copy uses
only Node built-ins and works while an old PR branch without the new tooling (or
with a different `core.mjs`) is checked out. Refresh the copy when the driver is
updated; its parity tests enforce the same behavior as the in-repo command.

```sh
mkdir -p "$HOME/.local/share/aeon-ci"
cp <repo>/scripts/test-tiers/tiers-merge-driver.mjs "$HOME/.local/share/aeon-ci/tiers-merge-driver.mjs"
# Replace /absolute/path/to/node with the absolute path from command -v node.
git -C <repo> config merge.tiers.driver '"/absolute/path/to/node" "$HOME/.local/share/aeon-ci/tiers-merge-driver.mjs" %O %A %B %P'
```

Git runs the driver through a shell, so `$HOME` resolves outside the checkout;
`~` does not expand inside the double-quoted script path. Use an absolute Node
executable path so a different shell PATH cannot select another runtime. For another install location, replace only the stable
script path in that configuration. The executable takes `BASE OURS THEIRS PATH`;
PATH selects one of the three manifest families. The repository's `.gitattributes` provides these attributes.
Also add the following lines once to `$GIT_COMMON_DIR/info/attributes` so branches
that predate `.gitattributes` use the driver. Resolve that directory with
`git -C <repo> rev-parse --path-format=absolute --git-common-dir`; preserve any
existing attributes rather than replacing the file.

```gitattributes
scripts/ci/go-test-tiers.json merge=tiers
scripts/ci/web-test-tiers.json merge=tiers
web/ci-web-shards.json merge=tiers
```

The driver accepts both old and canonical layouts. It merges keyed row sets:
independent additions survive; deletion wins over an unchanged row; deletion
versus change and divergent edits to one row (including tiers/weights) conflict.
Group specs merge independently. A unilateral reorder of surviving groups is
taken alongside the other side's content edits; matching reorders are taken,
divergent reorders conflict. New groups append in identity order. The same order
rule applies to integration notes. Launch policy and unrelated metadata changes
merge by field. Timing-owner records merge atomically.
A clean exit 0 means the canonical merged result has been written to OURS,
even when the inputs were unchanged old-layout manifests. Exit 1 means a real
disagreement: OURS contains deliberately invalid JSON with git-style diff3
conflict markers and the complete OURS, BASE and THEIRS versions. Unresolved
markers fail JSON parsing and the canonical check. Usage/parse/IO errors
(including malformed JSON, duplicate input identities, Go occurrence and
unsupported paths) exit 2; unexpected crashes exit 3. Those failures leave OURS
byte-for-byte untouched, so it may still be valid JSON. Every replacement,
including conflict output, writes a sibling temporary file, fsyncs it, then
renames it atomically. Failure before rename leaves OURS unchanged. Stderr names
the offending path/key. The driver never selects a tier or weight to resolve
contradictory edits. It is a local convenience, not a GitHub queue fix.

If `merge=tiers` is set but `merge.tiers.driver` is not configured, Git falls back
to its built-in text merge. A configured but missing driver script makes Node
exit 1 with `MODULE_NOT_FOUND`, which looks like a real disagreement but leaves
OURS untouched. **Any nonzero driver result means UNRESOLVED: read stderr and
never `git add` the working file as is.** Git keeps the path unmerged; a valid
JSON file alone does not prove that THEIRS' classifications survived.

`git checkout --merge -- <path>` re-runs the configured `merge=tiers` driver;
it cannot recover text conflicts independently of a missing or failing driver.
Instead, merge the three index versions directly with `git merge-file`, which
does not consult merge attributes. Use three regular temporary input files with
this POSIX-shell recipe (also bash/zsh): process substitutions such as `<(git show ...)`
can be read as empty files by `git merge-file` (verified with Git 2.55.0),
silently producing empty output and exit 0.

```sh
path=scripts/ci/go-test-tiers.json # Replace with the unmerged manifest path.
merge_tmp=$(mktemp -d "${TMPDIR:-/tmp}/tiers-reconcile.XXXXXX")
if git show ":2:$path" > "$merge_tmp/ours" &&
   git show ":1:$path" > "$merge_tmp/base" &&
   git show ":3:$path" > "$merge_tmp/theirs"; then
  merge_status=0
  git merge-file -p --diff3 -L ours -L base -L theirs \
    "$merge_tmp/ours" "$merge_tmp/base" "$merge_tmp/theirs" > "$merge_tmp/result" || merge_status=$?
  if [ "$merge_status" -le 127 ]; then
    cat "$merge_tmp/result" > "$path"
  else
    echo 'Text merge failed; working file left untouched' >&2
  fi
fi
# Inspect the result, then trash this run's temporary directory.
trash "$merge_tmp"
```

Confirm stages 1, 2 and 3 exist before using the recipe. `git merge-file` returns
0 for a clean text merge, a positive conflict count (at most 127) for conflict markers,
and a negative error code (reported as 255 by the shell) for a failure. Stop on
an error; conflict markers still require explicit reconciliation. Alternatively,
start from THEIRS with `git show :3:<path> > <path>` and re-apply OURS' missing classification rows using
`node scripts/test-tiers/cli.mjs classify --tier <T> ...`. Explicitly reconcile
conflicting tiers, weights, order and metadata against both index versions.
Finish with `node scripts/test-tiers/cli.mjs manifests --write` and
`node scripts/test-tiers/cli.mjs manifests --check`, then inspect the complete
resolved diff against both sides before staging.

## Local static CI pre-filter (OPS-257)

Before pushing, run `node scripts/ci-static.mjs --here` from any directory
(the script resolves its own worktree). Before enqueueing a committed branch,
refresh `origin/main` normally, then run `node scripts/ci-static.mjs --merge-main`
(the default). It merges committed HEAD with the locally cached `origin/main`
in a detached OS-temporary worktree, reports conflicts, and removes that
worktree afterwards. It never fetches, changes the caller's branch/files,
installs dependencies, or pushes. Uncommitted edits are included only by `--here`.

`--list` shows the registry and workflow origins, `--only id,...` selects checks,
`--json` emits one report with statuses, commands and seconds, and `--jobs N`
sets the bounded pool (default 4). Each check has a timeout. Failures include
the last 40 output lines and the exact command to reproduce. Exit codes are
0 for a complete pass, 1 for check/merge failure, 2 for usage/setup errors,
and 3 for an incomplete run with optional skips. `--allow-skips` explicitly
permits exit 0 with skips; the summary still says **INCOMPLETE**, names every
skipped check, and JSON carries `incomplete: true`. Failures take precedence.

Coverage mirrors the static Go, release, audit/slice ownership, runner guard,
migration policy/numbering, shard inventory/spec drift, planner and tier tests,
and OpenAPI property lint checks in `.github/workflows/ci.yml`. Web typecheck,
lint, native shard tests, browser safety tests and native tier collection are
optional when installed `web/node_modules` is absent; no browsers are launched.
When caller and merged `web/package-lock.json` bytes match, merge mode symlinks
the caller's installed dependencies into the disposable tree and treats them
as read-only. npm/tool caches and TypeScript build info go to temporary paths;
typecheck extends the original projects through temporary configs under `web`
to preserve module resolution. It never installs or shares mismatched locks.
Checks receive a small fixed environment: caller GOFLAGS and AEON_* settings
are stripped, CI/full-lane/full-tier values are set, and differing Node major
or Go major/minor versions from `ci.yml` generate warnings.
A dependency-free strict shard inventory still catches unregistered specs.
The migration baseline uses GitHub's **latest published stable release**, exactly
as CI does, and requires its local tag. Offline operators may explicitly pass
`--base-ref vYYMMDDhhmmss.0.0` after verifying that published release; there is
no fallback to the newest tag, which may be unpublished.

This is a pre-filter, never a gate. Hosted CI remains the proof, including DB
compatibility, full Go/browser suites, builds, Nix vendor hash and release checks.
The command registry is `scripts/ci/static-checks.json`. It explicitly classifies
every CI job as static or not-static (with a reason and pinned run-inventory digest).
Static jobs require a primary mirror or a reasoned exact exclusion for every
run line/block; supplements cannot satisfy coverage. Unknown jobs, new run
steps, changed excluded runs, unclassified lines and stale origins fail drift
validation and require an explicit classification decision. Not-static digests
are SHA-256 of JSON arrays of `{step, cwd, command}` for every nonempty run in
workflow order, with commands normalized by the workflow reader; review the
changed runs before updating a pin.
`release-check-run` already includes `scripts/ci-static.test.mjs` in its
**PR classification and exact-SHA reuse regressions** command; the registry
mirrors it and the historical Go workflow assertion pins that accepted command.
That workflow is owned by the coordinator. Root `scripts/*.test.mjs` files use these explicit CI commands,
not the Go or web tier manifests (web collection covers `web/tests/`).

Disposable worktree creation and merging override caller hooks, signing and
fast-forward settings. Interrupts cancel process groups and clean this run's
worktree. Cleanup failures are reported without replacing the original result;
pruning is attempted afterwards. A reported residue path is this run's detached
worktree: inspect it, then use `git worktree remove --force <reported-path>` and
`git worktree prune`. Do not remove other workers' worktrees.
