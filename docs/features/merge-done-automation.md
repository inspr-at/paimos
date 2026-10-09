# Merge → Done automation

An authenticated GitHub App observation of a PR merged into `main` moves every
AEON work item named in its title or branch to Done. Queue checks and group
destruction alone do not prove a merge: the existing installation reader checks
the canonical PR document. Queue constituents survive deleted queue refs in the
authenticated event ledger. Main pushes provide a second observation path.

The final transaction takes the tenant, pairing and tree fences, locks the
ticket batch in UUID order, and checks `nodes.write` again inside that
transaction. The delivery installation remains the actor of record. Authorization
uses an existing person and creates no role, key or scope:

1. The configured lead owner (`project_lead_settings.owner_person_id`, otherwise
   `project_leads.owner_principal_id`). If that person is set and no longer has
   `nodes.write`, completion is refused. Nobody else is substituted.
2. Otherwise the responsible person who already holds `nodes.write`: at most 16
   active unlinked people with a project role binding (admin, then member, then
   any other project role), then at most 16 workspace owners, admins and members
   in that order. Ties break by binding creation time, then principal id.
3. Otherwise the active unlinked person who created the ticket (`node.created`),
   if they still have `nodes.write`.

Agents are never the fallback. If nobody qualifies, the merge adds one
non-applicable **Needs You** proposal ("no active project owner with
nodes.write") and leaves the ticket unchanged. Parent statuses remain derived.
Running or queued workers, pending handover, human checks, cancellation,
archival and missing benefit text refuse completion. Existing Done, Delivered
and Accepted work does not regress.

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
human-check protection, a waiting split with no live session or run,
permission revocation during the external read, dry-run/apply replay, stale
revisions, open follow-up PRs, partial reads, and published membership.
Existing assertions and CI gates are retained.

Validation of code candidate `d92d288ac`: the remote merge-main static gate
returned 0 (40 checks passed, no optional skips). Delivery, Status Autopilot and
authorization, pairing and reporter contract package tests passed remotely.
The contract-dependent remote run generated OpenAPI before testing. The tier/shard/ownership unit checks passed
with 210/32/33 tests, and the shared-fence inventory passed remotely.

Fix round 2, `7ff41ce06`: merge completion also refuses when `aeon_work_pending`
is set and no session or run is live. The refusal is one non-applicable Needs
You item and the ticket state stays unchanged. Remote `./internal/delivery`
passed on mbp2606. The remote merge-main static gate returned 0 (40 checks
passed, no optional skips). Tier, shard and audit units passed 210/32/33.
No origin push.

Owner fallback, `d3ced0ca5`: a project with no lead settings and no lead
principal completes through an existing person who already holds `nodes.write`
(project role binding, then workspace owner, admin or member, then the ticket's
creating person). A configured owner who lost that grant still refuses, with no
substitute. No person with the grant, including while an agent still has it,
writes one non-applicable Needs You proposal and leaves the state unchanged.
No role, key or scope is created. Local and remote `./internal/delivery` passed
on mbp2606 (`TestMergeDone` included). The merge-main static gate returned 0
(41 checks passed, no optional skips). No origin push and no live backfill.
