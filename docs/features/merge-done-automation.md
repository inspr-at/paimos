# Merge → Done automation

An authenticated GitHub App observation of a PR merged into `main` moves every
AEON work item named in its title or branch to Done. Queue checks and group
destruction alone do not prove a merge: the existing installation reader checks
the canonical PR document. Queue constituents survive deleted queue refs in the
authenticated event ledger. Main pushes provide a second observation path.

The final transaction takes the tenant, pairing and tree fences, locks the
ticket batch in UUID order, and checks the current project owner's `nodes.write`
permission. The configured lead owner is used; no service role, key or scope is
created. Parent statuses remain derived. Running or queued workers, pending
handover, human checks, cancellation, archival and missing benefit text refuse
completion. Existing Done, Delivered and Accepted work does not regress.

A refusal adds a deduplicated informational proposal to **Settings → Status
Autopilot → Needs You**, naming the ticket, PR and gate. It can be dismissed;
it cannot apply a state change. Fix the ticket gate and redeliver the webhook
to retry. A successful retry clears that merge's pending refusal. State changes
produce the usual `node.updated` event and a `delivery.merge_done` receipt.
Receipts survive projection rebuilds, restarts and webhook redelivery.

Historical repair uses `POST /api/projects/{projectId}/delivery/merge-backfill`.
Supply up to 100 PR numbers found by a read-only merged-PR inventory. The default
request previews the list and records `delivery.merge_backfill_preview`, without
changing tickets or creating refusal proposals:

```json
{"pull_requests":[411,412],"target":"done"}
```

Inspect the returned `items`, `not_merged`, gate reasons and `preview_event_id`.
Then send the same request with `apply:true` and that event ID. Only the preview's
caller may apply it. GitHub merge facts, the complete bounded open-PR inventory,
ticket revisions, permissions and gates are read again. A changed preview
returns 409 and needs a new dry-run. Apply commits once and replays its original
result. `applied:true` means the batch committed; each item's `result` reports
whether it changed, was already complete, or was refused.

`target:"delivered"` additionally requires `release_id`. The server verifies
frozen published release membership and that publication followed the PR merge;
a tag date or current planning membership is insufficient. This reads the
existing publication store without extending retired journey/stage features.
Ordinary release publication continues to promote Done work as before.

The backfill uses existing `delivery.manage` and `nodes.write` grants. Bodies,
references, ticket batches, external inventories and operation time are bounded;
partial external reads fail instead of authorizing repair. It makes no GitHub
write and grants no merge, publication or deployment approval.

AEON-1008 delivery evidence: the reported baseline was 58 stuck tickets found
after release 126 (34 In progress, 21 QA, 3 Done). Arion W2's context was 62.3%
first-attempt workflow green and 120-minute median PR open-to-merge; this change
does not claim to shorten CI execution. Instrumentation is the merge receipts,
pending refusal proposals, preview/apply audit events and per-ticket outcomes.
The seven-day outcome owner is AEON-LEAD: after deployment, compare confirmed
main merges with ticket completion receipts, report remaining stuck work and
refusal reasons on AEON-1008. No historical production repair is executed by
this implementation branch.

Correctness coverage uses checked-in sanitized App-shaped payload fixtures for
PR closure and merge-queue events, plus canonical reader validation. It checks
redelivery, multiple queue constituents, refusal visibility/recovery, parent and
human-check protection, permission revocation during the external read,
dry-run/apply replay, stale revisions, open follow-up PRs, partial reads, and
published membership. Existing assertions and CI gates are retained.
