<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Delivery evidence feed

Delivery numbers consume existing authenticated harness submissions, explicit
merge resolution, OPS rollout records, completed independent audit reports and
the GitHub run/attempt feed. No UI, permission, execution flag or deployment
approval changes. Measurement facts never authorize a review, merge or release.

## Sources and replay

- **Reviews:** the metric reader joins submitted `review_runs` to their
  reviewer-authored `coordinator_review.recorded` events, with explicit project
  and repository bounds. Existing history is readable immediately; an old Flow
  cursor does not hide it. Creation → submission measures review time. Only the
  first verdict per head counts, including when a report or `aeon/review` status
  describes that same head. Unsubmitted runs and legacy coordinator assertions
  produce no sample. Missing external gates keep review coverage partial.
- **Merge rounds:** trusted merge tooling can submit `merge_round` facts with
  outcome `scripted` or `model` through the existing facts route. The existing
  shadow queue's progress route also accepts optional `merge_outcome` when a
  claimant completes a merge round. It requires a current project/repository/
  ticket/PR link and commits the measurement with progress. Omitting it invents
  no classification. The queue's execution flag stays closed.
- **Release to live:** a forward `aeon.rollout.v1` report with successful/live
  outcome, `cut_at` and `live_at` emits a release fact when the project has a
  linked metric repository. `healthy_at`, when present, remains separate.
  Publication, healthy time alone and in-progress/failed reports do not invent
  live confirmation. Identity is `rollout/<release label>`, not version. The
  existing `release_queue_to_live` key measures the recorded start → live: OPS
  cut time or a manually reported queue-entry time. The server label and
  definition name that distinction.
- **Review audits:** the existing independent-audit producer can submit its
  validated facts with `--submit` (below), through existing CLI authority.
  A failed or partial receipt fails the command and retains the prepared file.
- **Preflight:** AEON-1658's existing webhook/backfill ingestion supplies the
  canonical run and attempt. Its first completed attempt also emits a
  project-scoped `delivery.evidence.preflight` event with content-free workflow
  identity, times and observed conclusion. This is a workflow observation;
  `workflow_head_sha` is not an assertion about a candidate's receipt. No new
  jobs ingestion, CI sample, runner-wait estimate or backfill controller exists.
  Preflight coverage retains the existing independent workflow coverage rules.

Changed facts and their typed `delivery.evidence.review`, `.merge_round`,
`.release`, `.review_audit` or `.escaped_defect` events commit atomically.
Repeated identical facts emit no event; corrections update one canonical row
and append a new observation. Replaying webhook/backfill preflight evidence is
also idempotent. Event timestamps record receipt; payload timestamps retain the
measured work. All resource locks precede the event counter.

`POST /api/projects/{projectId}/delivery/metrics/facts` still needs
`delivery.manage`. Its optional `repository` captures the source used by the
producer; a relinked project returns 409 and writes nothing. Existing callers
can retain their original payload. Reports remain limited to 100 facts/64 KiB.
No new table or migration is introduced by this feed; it builds on AEON-1658.

## Coordinator tooling

After actual work has completed, retain a bounded `{ "facts": [...] }` file
using the existing fact schema. Use stable keys identifying the gate run,
merge round, release or audit; report the actual outcome and times. Supply the
repository captured with that work, rather than looking up a replacement after
failure. This command only publishes measurements with existing authentication:

```sh
node scripts/delivery-evidence.mjs \
  --aeon /Users/markus/.aeon/lead/aeon/pm --instance ppm \
  --project PROJECT_UUID --repository inspr-at/paimos --input completed-facts.json
```

For weekly audit results, keep the existing plan and full independent reports.
The producer validates the complete sample and cross-family attribution before
sending its content-free measurement facts:

```sh
node scripts/review-audit.mjs facts \
  --plan weekly-plan.json --results completed-audits.json --output audit-facts.json \
  --submit --aeon /Users/markus/.aeon/lead/aeon/pm --instance ppm --project PROJECT_UUID
```

Without `--submit`, preparation retains its previous offline behavior. Retry a
failed submission with `delivery-evidence.mjs` and the retained facts file; no
new key, secret, role change, gate invocation or status mutation is needed.
A success requires a receipt confirming the complete fact count.

## Merge audit provenance repair

The observe-only merge audit now reads authenticated harness review runs as
well as existing work-order reviews. It verifies the immutable range, PR,
repository, project, independent model families, reviewer event and submitted
capability before the merge. A changed head still requires the existing clean
Git proof; a reported review metric cannot satisfy provenance or admission.
Current admission continues to enforce its existing live binding checks.

When queue provenance is missing, a fixed, read-only GitHub GraphQL query reads
up to ten timeline pages with the existing installation's `pull_requests:read`
and `metadata:read` grants. It requires the canonical PR's exact merge SHA and
an enqueue episode ending with removal reason `merged`. An old enqueue, manual
removal, another PR/SHA, GraphQL error or incomplete timeline proves nothing.
Checks then refer to the canonical merge commit, including when its SHA differs
from a transient merge-group head. No token or query can mutate the queue.
Existing flagged rows are repaired by the normal audit reconciliation.

The acceptance cases are PR #874 (queue + authenticated harness review after
capture began) and PR #847 (queue history before capture). For
`inspr-at/paimos` merges before **2026-10-22 10:44 UTC**, an unavailable verified
review is classified `before_evidence_capture` rather than `no_review`; it is
not marked reviewed. The cutoff applies only to this repository. Unrecorded
post-cutoff reviews remain unverified: a QA file or a coordinator's assertion
is not substituted for an authenticated reviewer submission.

Deployment, production backfill and external coordinator hook rollout remain
coordinator/OPS operations. This worker runs neither gates nor live backfills.
The AEON-1658 seven-day repair instructions remain in
[Delivery history jobs](delivery-history-jobs.md).
