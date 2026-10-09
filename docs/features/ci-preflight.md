# Exact-commit preflight (AEON-1017)

Preflight adds Go/unit and hosted browser evidence before a builder opens a PR
or updates its head. CI, every required check, cross-family review, human gates
and deployment controls remain in force. Shipping remains off/shadow; a green
receipt never grants execution, approval, push, merge or deployment authority.

The reviewed controller must be installed outside the candidate checkout. On
the approved mbp2606 test lane, in a clean checkout of the candidate commit:

```sh
node /path/to/reviewed/scripts/ci-preflight-local.mjs "$candidate_sha" "$base_sha" "$receipt_file"
```

`base_sha` must be the checkout's resolved `origin/main`, an ancestor of the
candidate. The runner checks HEAD and cleanliness before and after each step:
the existing static bundle (including vet, bootstrap drift and selector
regressions), strict Go classification, every test in changed Go packages,
full web units and strict web classification. Global Go inputs and over-cap
package changes widen to all packages. Failed, interrupted and unexecuted
checks produce a red receipt. No command output, environment, credential,
private payload or machine address enters the receipt.
Affected Go package execution requires the approved lane's explicit loopback
test database (`AEON_TEST_DATABASE_URL`, database name `aeon_preflight_<sha>` or
`aeon_run_<sha>`, with a 10–40 character lowercase hexadecimal suffix). It
refuses to call package tests without it, so integration tests cannot silently
skip because the database variable was dropped. Database creation/cleanup
remains with the approved test-lane controller.

The authorized builder dispatcher supplies that receipt as `local-result` to
`ci-preflight.yml`, dispatched from reviewed **main**, with `sha` naming the
exact candidate. The local receipt is the authorized builder's attestation,
not an independently signed runner proof: dispatch authority must remain with
the controller that ran it on mbp2606. Never permit an untrusted candidate to
dispatch an invented receipt. No new credential or signer is introduced.

The candidate must already exist on GitHub. For a new head on an existing PR,
the owning launcher stages the immutable commit on its temporary preflight
ref, runs preflight, checks admission, then updates the PR branch. It must not
update the PR head merely to make the candidate available to preflight. This
worker adds no git-writing launcher capability.

The workflow uses `test-runner-route.yml` outputs, requires a current-attempt
**hosted** route and retains the tier runner's hosted-only browser guard. A
non-hosted route refuses execution and admission stays closed. OPS owns
router/pool policy and must confirm hosted capacity before rollout. Each of
12 full-layout browser groups has a stable job name and an individual
SHA/run/attempt receipt. WP1.4 may replace full selection only after its
complete, base-tree-bound impact planner is available; no current selector is
assumed complete. Setup failures cannot create a green group result.
Dependencies, web build and browser downloads run once in a routed setup job;
groups share that attempt's runtime and install only their OS libraries.
The draft adds a narrow repository runner-guard policy for this workflow's
dispatch, distinct job IDs, read-only authority, per-head concurrency and
main/hosted/current-attempt conditions. This policy addition also requires OPS
review; existing CI and runner-side pool admission are unchanged.

The result job assembles `preflight.json` in the artifact
`aeon-preflight-<sha>-<attempt>`. Candidate code runs in a separate checkout
from the reviewed workflow controller. Permissions are read-only and no
secret other than existing GitHub authentication is used.

The installed admission checker uses existing `gh` authentication:

```sh
ci-preflight-admission check --repository OWNER/NAME --sha "$candidate_sha"
```

It prints a value-free `paimos/admission` decision and exits nonzero on denial.
The owning launcher must call it immediately before PR creation and every
head update, using the same captured SHA for the action. The PAIMOS shipping
claim path also reads preflight using its existing read-only GitHub App and
records the refusal reason after rechecking current authority and review
bindings in the final transaction. The required-check reporter (WP1.5) can
call `delivery.LookupPreflight` with the same repository-bound readers; OPS
owns required-check/ruleset activation. This checker never posts a status.

Missing, red, pending, expired, malformed, duplicate, stale-SHA, stale-attempt
and partial evidence fails closed. Lookup examines at most 1,000 workflow
runs, rejects a truncated inventory, and never falls back from the newest
matching run to an older green. Artifacts are limited to 64 KiB compressed
and one 32 KiB JSON entry. Installation authority is not forwarded to signed
artifact storage URLs.

`ci-preflight-report.mjs` emits `preflight_red_rate` separately: red completed
preflight attempts / all completed preflight attempts, including setup failure
without a receipt. Pending attempts are counted separately; no observations
mean a null rate. CI's PR-only `first_attempt_green` calculation is unchanged.
The Delivery page fit work (WP1.1) owns displaying this line beside that tile.

`tier-measurements` now reports evidence categories: upstream setup/planner
failure, skipped required tier jobs, missing artifacts, identity mismatch,
invalid runner timestamps or case evidence, reuse conflicts, malformed JSON,
API failure and unclassified errors. These are observed failure categories,
not retrospective claims about the historical 49 red PR runs or 22 queue runs.
Its post-execution checks and exit codes remain; OPS owns promoting a repaired
aggregate to a required check with the nixcfg pin.

The saved Arion W2 attempt-1 inventory (2026-10-05 00:00 UTC through
2026-10-09 04:00 UTC) reproduces this mutually exclusive breakdown:

| Observed cofailure | PR runs | Queue runs |
| --- | ---: | ---: |
| Setup or planner also red | 8 | 3 |
| Tier execution also red, with no setup/planner red | 22 | 3 |
| No setup or tier-execution red in inventory | 19 | 16 |
| Total tier-measurement red | 49 | 22 |

Reproduce with `node scripts/test-tiers/measurement-causes.mjs RECORDS_JSON W2`
against the coordinator's saved `measure-v2-records.json`. This inventory only
contains failed job names: the cofailures are evidence, but artifact/API/log
causes for the 19 PR and 16 queue cases remain unclassified. The new cause
codes make those failures distinguishable on future attempts without inventing
a historical explanation.

Coordinator PR copy: W2 baseline `first_attempt_green` **62.3% per commit**;
instrumentation is the separate preflight attempt rate plus measurement cause
codes. Correctness evidence covers SHA/tree mutation, missing/red/stale results,
partial lookups, current attempt binding, hosted routing and archive bounds.
LEAD owns the 7-day outcome after deployment and the ≥70% two-week target;
OPS owns runner capacity, review of this draft workflow and aggregate/ruleset
activation. No hosted run, production outcome or live deployment is claimed
by local tests.

GitHub's [dispatch identity](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_dispatch)
belongs to the dispatched workflow ref, so the candidate SHA is bound inside
the receipt. [Artifact metadata and download semantics](https://docs.github.com/en/rest/actions/artifacts?apiVersion=2026-03-10)
supply the workflow run binding and signed archive redirect used by admission.
