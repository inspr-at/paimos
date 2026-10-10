# Develop

```sh
just db-up        # Postgres 18 + pgvector on :55432 (Docker)
just test         # Go tests
just web-check    # web typecheck and build
just dev          # run the server (API on :8080); `cd web && npm run dev` for the UI
```

Event ordering and presentation race tests observe database lock waits. Importer
request tests use held transports and `testing/synctest` virtual time;
elapsed time is not evidence that an operation started. Doctrine quotation guards
use synthetic public and private fixtures in ordinary runs. To survey current
doctrine checkouts, opt in:

```sh
AEON_TEST_DOCTRINE_CHECKOUTS=1 go test -count=1 -run '^TestCheckedOutDoctrineGuardCounts$' -v ./internal/rules/doctrine
```

The survey reports observed counts without requiring historical corpus totals.

CI runs on pushes to `main`, pull requests, merge-queue check requests and manual
dispatches. A new push to `main` cancels superseded main CI runs to free runner
capacity (AEON-585). Main's group is separate from PR and merge-queue groups:
PRs keep their existing per-PR cancellation, while each queue and manual run
keeps a unique group. Required checks remain `go`, `web`, `release-check` and
`e2e`; the external `gate/cross-family` status is unchanged.

Test tiers (AEON-681): full-lane PRs and merge groups run ESSENTIAL plus changed-area
cases; Go reverse dependencies add at most 300 extra cases. There are three tiers:
ESSENTIAL is the approved core; GATED-FULL retains the other previously gated Go,
unit and browser cases; NIGHTLY includes the never-gated browser catalogue.
Shared inputs, uncertain impact, main/manual CI and explicit `--full` select
exactly ESSENTIAL + GATED-FULL. Browser gate membership comes from OPS-257's
`gate:true` groups in `web/ci-web-shards.json`, with approved ESSENTIAL promotions
preserved. Mapped changed-area selection still includes optional cases in the
affected area. Unknown impact widens to the established full gate.
Reports name the selection `scope` (`gated-full`, `catalogue` or `changed-area`) and count
`deferredBrowserCases`; deferred cases are not reported as passes or skips.
Classifications live in the [Go manifest](../../scripts/ci/go-test-tiers.json)
and [web manifest](../../scripts/ci/web-test-tiers.json). Use
`node scripts/test-tiers/cli.mjs classify go` (or `web`) for new NIGHTLY cases,
then `check --strict`. CI and nightly require `check go --strict` in their static
Go job and `check web --strict` before the web build. Every native case must have
an explicit classification; missing and stale entries fail these checks. The
independent scheduled inventory audit remains available. Reconcile stale
registrations against the native inventory when updating manifests, preserving
the tier of every surviving case. Malformed entries and known-flaky ESSENTIAL cases still
fail validation. New browser specs also need a
launch policy in `web/ci-web-shards.json`. No tests are removed: deletion tags
remain on nonessential cases, including candidate groups whose exact members need review.
The [known-flaky registry](../../scripts/ci/known-flaky.json) names exact case keys
and owner tickets; its validator rejects any ESSENTIAL entry. The nine cases
identified in AEON-675/676/683 stay outside ESSENTIAL and retain their prior
full-gate membership as GATED-FULL, plus changed-area and nightly coverage.
Scheduled/manual `nightly-full.yml` uses `--all` to execute every
case, including the optional browser catalogue; main/manual CI runs the full gate
unless exact-SHA full-suite reuse is verified below. The main-push run is the
FULL gate after merge; a full premerge run is required only when impact is
uncertain. Releases require a green full main-push gate (or full nightly run) on the
exact freeze SHA, in addition to the release rehearsal. Nightly is separate from
the required PR/main checks; its failures remain visible for classification.
[Nightly CI evidence and reporting](nightly-ci.md) describes the reporter and its ownership.
A red nightly needs a ticket naming its failing cases and tested SHA. The existing
PAIMOS reporter can invoke `node scripts/nightly-ticket.mjs --run-id ID` for a
read-only preview. To deliver it, supply its existing absolute CLI path with
`--paimos PATH`, its shared state directory with `--state-directory PATH`, and
`--write`. The reporter verifies the PPM/INSPR account, binds the complete job
inventory to the run's SHA and attempt, and deduplicates by run before creating a
hidden bug ticket. A per-run lock serializes writes; an uncertain write is an
error, never success. No PAIMOS credential is added to Actions. OPS step 2 owns
activation in the existing reporter, ownership and budget wiring.
Tier artifacts now include bounded `*-failures.json` case evidence. Download the
current attempt's artifacts and pass their directory with `--evidence PATH`;
evidence for another SHA or attempt is rejected. Without case artifacts the
ticket explicitly identifies only failed jobs and the missing case evidence.
The AEON lead collects the `nightly_green` outcome seven days after deployment,
with OPS-257 covering reporter and runner infrastructure. Migration compatibility
always runs fresh; timing, static and smoke gates stay outside tier impact
filtering within the full CI lane. Stability cases retain their manifest tiers
and changed-area/full coverage. Selected cases never retry. `test-tier-run-measurement` reports cases
per class and complete job costs against the 22.50 Go / 42.17 web runner-minute
baselines; reused reports carry provenance without claiming fresh passes.
Web setup shares the built runtime before four file-preserving unit shards and
twelve full-gate browser shards run in parallel. The required `web` aggregate
gates setup and both matrices; spec-only retains one unit job and one exact-spec
job, while docs-only skips them. Skipped Actions jobs contribute no runner duration;
executed jobs still reject invalid timestamps. Spec-only measurements explicitly
report `untiered` scope rather than claim tier case passes.
OPS-257 schedules tiers using hosted run 37377299349: browser `tierTiming` in
`web/ci-web-shards.json` and unit/Go `timingWeights` in their tier ledgers.
Browser costs include per-case durations and apportioned group overhead; unit
costs include Vitest elapsed time, Node test time and shared residual launch
overhead; Go costs include terminal package elapsed time plus shared residual
native enumeration and launch time. Other selections scale
these measured slices by case count. Missing browser/Go artifacts retain
historical estimates; exact-spec weights and all gated membership stay intact.
The 39 `aeon-632b-clip` cases keep their original bodies and titles in two
serial files: planning/touch (27 cases) and settings/rules (12), importing
shared fixtures. Each half conservatively retains the original launch overhead.
Unit shard 1 loses equal-load ties to its peers because it also runs the shard
and browser-supervisor regressions; the heaviest unit file starts on shard 2.
Scheduling estimates after native inventory reconciliation are 6.28–6.39 browser
minutes (max/median 1.012), 0.50–0.74 unit minutes and 4.85–6.12 Go minutes
(max/median about 1.26).
The indivisible `internal/nodes` package sets the Go floor, slightly above
the 1.25 execution-only ratio target. Estimates exclude runner setup, initial
selection and shard 1's extra unit checks; hosted runs must validate complete
job costs and the intended eight-to-nine-minute critical path.
The eight-minute target needs hosted measurement; [local selection counts](../../scripts/ci/test-tier-selection-baseline.json)
establish coverage only.

`scripts/ci-flake-guard.mjs`, `scripts/ci-quarantine.json` and
`scripts/ci-flake-report.mjs` are retained for NIGHTLY flake diagnosis and
OPS step 2. The current nightly tier runner executes all cases with zero
retries and does not invoke the guard or suppress quarantined failures;
these tools do not soften PR or merge-queue checks.

PRs containing only root Markdown, `docs/**` (excluding `testdata/`), or root
LICENSE/LICENCE/COPYING/NOTICE files skip classified heavy jobs; required checks
report success with a `docs-only` note. Migration compatibility runs in every
lane, independently of classification. The allowlist lives in
`scripts/ci-pr-plan.mjs`; CI executes the PR base commit's copy, and an unavailable
base classifier retains full validation. Markdown under implementation and
test-fixture paths requires full coverage. PRs changing only top-level
`web/tests/*.spec.ts` run exactly those specs in one hosted job, including specs
outside the shard map, with their group's config and flags. Spec renames or
deletions, mixed changes and unavailable classification retain full coverage.
Main pushes and manual CI runs execute ESSENTIAL + GATED-FULL; merge groups use tier selection
within the full CI lane. Docs-only and spec-only classification applies only to PRs.

The separate release rehearsal already cancels superseded runs per ref. Release
tag builds still require a successful `release-image-check.yml` rehearsal for
the exact release SHA on `main` (push or manual dispatch); a cancelled rehearsal
does not satisfy that gate. Main validates the full suite unless the exact-SHA
proof below establishes full merge-group execution. Workflow policy tests live in
`scripts/ci-runner-guard` and `scripts/releaseworkflow`.

The ordinary activity tests check exact pagination through 240 same-ticket
imported history snapshots and 30 Markdown comments alongside 27,422 unrelated
imported events, plus the node index definition. They impose no latency budget.
For a controlled performance check, use an idle dedicated test runner with local
Postgres and run without the race detector:

```sh
AEON_TEST_DATABASE_URL="postgres://aeon:aeon@127.0.0.1:55432/aeon?sslmode=disable" \
AEON_ACTIVITY_IMPORT_SCALE_PROBE=1 \
go test -count=1 -run '^TestTimelineAtImportScaleLatency$' -v ./internal/activity
```

This opt-in probe uses the same synthetic fixture, excludes setup, warms up five
first-page requests, and samples 30 sequential requests with a page size of 20.
It measures the handler and JSON decoding; the nearest-rank p95 must be below
100 ms. Median and maximum are logged for diagnosis. It does not measure full
history traversal or guarantee production latency. Shared or loaded runners
are unsuitable for interpreting this budget.
