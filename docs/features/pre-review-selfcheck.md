# Builder self-check and weekly review audits

Before requesting independent review, the builder commits the assigned work,
checks it against the repository's review rules, and attaches sanitized evidence
to the ticket. The PR template carries baseline, instrumentation, correctness
tests and a follow-up owner. These tools validate evidence completeness and
snapshot identity; they cannot prove the builder's assertions. Independent
cross-family review, required CI, human approvals and release gates still apply.
No model is launched, review verdict posted, ticket changed or metric submitted
by either tool.

## Builder evidence

From the assigned worktree with a clean, committed tree:

```bash
mkdir -p tmp/review
node scripts/review-selfcheck.mjs init --output tmp/review/selfcheck.json
# Fill every pending item in that JSON with concrete, value-free evidence.
node scripts/review-selfcheck.mjs check --input tmp/review/selfcheck.json
```

The default comparison is `origin/main...HEAD`; `--base REF` pins another local
comparison ref. Fetching and merging remain coordinator responsibilities. Both
head and base commits and the complete changed-path list must match on check.
Dirty or untracked source, missing rules, a failed/pending/skipped test, missing
baseline/instrumentation, and an absent follow-up owner fail with exit 1. The
follow-up is due seven days after deployment. An unchanged code-health area may
be `not_applicable` with a specific explanation; tests, ownership and preservation
of gates always require `pass`. Commands and evidence are descriptions and are
never executed. `ready_for_review` means only that the builder evidence is
complete for that snapshot. Attach the JSON and check result to the ticket and
link them in the PR; run it again after every changed head or comparison base.

The checklist includes current-target authorization inside the write, global
lock order, bounded inputs, record identity, honest partial results, meaningful
behavior tests, stable controls, additive contracts/migrations, ownership and
preservation of gates. Evidence files stay in ignored `tmp/`, never in source.
Use file/test identifiers and sanitized reports, never credentials, private
prompts or raw logs. Input is capped at 1 MiB, regular files only, with no
symlink following; output refuses to overwrite an existing file.

## Weekly audits

`review-audit.yml` runs Monday at 06:00 UTC and can be dispatched on main. It
uses the existing read-only GitHub authority to upload an audit-plan artifact
for the previous complete UTC Monday–Monday week. It has no status, issue or
deployment write authority. Scheduling produces work to audit, not an audit
verdict; the coordinator must dispatch the independent reviewers.

The coordinator can also prepare the plan locally with existing GitHub auth:

```bash
node scripts/review-audit.mjs plan --repository example/aeon \
  --output tmp/review/weekly-plan.json
```

Collection paginates closed PRs ordered by update time until exhausted or
strictly before the window, retaining only merged PRs within `[start,end)`.
Each 25-PR page read is capped at 1 MiB and 15 seconds; the 20-page (500-PR) cap,
duplicate or unordered results, an invalid merge/head binding or any read failure fail the
plan. A partial collection never becomes an empty or complete population.
An offline `--inventory PATH` accepts `{schema:1,repository,window,complete:true,
pulls:[{number,head_sha,merge_sha,merged_at}]}`; the caller must establish its
completeness before using it.

The default sample is five PRs, selected by the lowest SHA-256 rank of repository,
week start, PR number and merge commit. This is a reproducible sample, not
risk weighting or coverage of every merged PR. A smaller population selects
all; zero merged PRs produces an empty plan, never a clean audit fact. The
coordinator can choose `--sample 1..20` before dispatch; keep that plan unchanged
for the week's reports and corrections. The artifact records population,
window, selected commits, complete inventory and a pending `results_template`.

The coordinator owns assignment, family/session attribution, the weekly
summary, recording findings as tickets and reporting completed audits. Re-read
each selected merged change and its original review, with the repository's
review checklist and [severity rubric](../../scripts/audit/rubric.md). Use an
auditor from another family than the original reviewer, in a distinct session.
The tool accepts canonical family IDs from the review gate (`openai`,
`anthropic`, `xai`, `cursor`, `google`, `local`); a harness alias cannot claim
independence. Verify actual attribution against the original review reports;
declared strings alone are not identity proof. The coordinator follows existing
reviewer-selection and human controls; builders never launch review gates.

Copy `results_template` into a separate result JSON and complete each audit:

```json
{
  "schema": 1,
  "audits": [{
    "pull_request": 42,
    "head_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "merge_sha": "cccccccccccccccccccccccccccccccccccccccc",
    "auditor": "audit-session-reference",
    "auditor_family": "openai",
    "original_reviewer": "original-review-session-reference",
    "original_reviewer_family": "anthropic",
    "at": "2026-10-05T06:00:00Z",
    "status": "completed",
    "evidence": "ticket attachment containing the independent audit report",
    "follow_up_owner": "delivery coordinator",
    "findings": [{
      "severity": "medium",
      "evidence": "changed path and the verified failing behavior",
      "ticket": "AEON-1021",
      "owner": "assigned finding owner"
    }]
  }]
}
```

Use the actual selected PR/commits, ticket and timestamp; `findings:[]` is clean
only with an explicitly completed independent report. Every finding needs a
ticket and owner; deduplicate known findings and link the existing ticket.
Missing, pending, duplicate, same-family or stale reports fail. Reports must
follow the merge and the end of the audited week; future and over-400-day-old
reports are refused. Full-week sample completion is required to produce facts.
Critical findings remain critical in the retained report and require immediate
coordinator escalation under the rubric; Delivery's existing enum counts them
as high. No review or release approval follows from an audit fact.

```bash
node scripts/review-audit.mjs facts --plan tmp/review/weekly-plan.json \
  --results tmp/review/weekly-results.json --output tmp/review/weekly-facts.json
```

Selection is recomputed from the plan's retained inventory. The generated
`{facts:[...]}` payload fits the existing
`POST /api/projects/{projectId}/delivery/metrics/facts` route, kind `review_audit`,
with the highest severity per audit (`clean|low|medium|high`). The coordinator
retains the plan and full reports as ticket attachments, verifies attribution,
then submits through the designated account. Confirm the API write before
claiming the metrics were recorded. Keys bind repository/week/PR; resubmitting
the same audit key corrects its earlier report instead of counting twice.
The tooling does not contact that API.

## AEON-1021 validation and follow-up

Baseline: builder self-check evidence and a weekly audit producer were absent;
Delivery already supported reported audit facts. Correctness tests in
`web/tests/review-tooling.test.ts` cover current-SHA binding, partial evidence,
bounded files, pagination failure, deterministic selection, cross-family
attribution, incomplete reports, maximum severity and API payload shape.
Instrumentation is the generated plan's population/sample, completed audit
facts, Delivery's `review_audits`, `review_changes_share` and `review_time`.
No pre-deployment improvement is claimed. The delivery coordinator owns the
seven-day post-deployment comparison: report self-check adoption, review rounds,
time and audit findings together, retaining unknown/partial coverage and
watching escaped defects. The initial weekly artifact awaits coordinator
dispatch and completed audits after merge; deployment and that follow-up are
outside a builder's handoff.
