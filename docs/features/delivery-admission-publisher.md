<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Merge-queue admission publisher

P3 connects the P2 admission evaluator to explicit GitHub queue operations and
one durable `aeon/admission` publisher. It ships disabled. Two host switches
select what runs. Both default off. Only the exact value `true` enables a
switch. OPS provisions the App and configures its private key path; the code
reuses the existing configured-path reader. No App provisioning, ruleset
cutover, bypass grant or deployment is part of this change.

`AEON_DELIVERY_ADMISSION_ENABLED=true` starts the status publisher and
reconciler for the repository and tenant already bound by `AEON_REVIEW_APP_*`.
That lane posts and re-reads only `aeon/admission`. It does not enqueue,
dequeue, quarantine, or open a queue Desk notice.

`AEON_DELIVERY_ADMISSION_QUEUE=true` also starts the queue lane. Leave it unset
until the App grant includes the queue write permissions below. With it off the
queue lane never starts: no installation token asks for `pull_requests:write`
or `contents:write`, nothing calls enqueue or dequeue, and a required failure
does not quarantine the head or open a queue Desk notice. A hold during a
merge-group build is published as `aeon/admission=failure` on that group SHA.
The harness keeps the enqueue (`expectedHeadOid` = the reviewed head). With the
switch on, enqueue, dequeue, quarantine and the queue Desk notice behave as P3.

The App adapter accepts only admission statuses and, when the queue switch is
on, explicit enqueue/dequeue. Enqueue sends `enqueuePullRequest(expectedHeadOid)`
with the admitted PR head. The adapter requests repository-limited installation
tokens, checks the granted repository and exact permission set, and revokes
tokens after use. The existing observation channel keeps its independent
read-only token. No production GitHub writes run in tests.

App permissions each mode needs:

| Mode | Installation token scopes |
| --- | --- |
| Status only (`AEON_DELIVERY_ADMISSION_ENABLED=true`, queue switch off) | Status read or write: `statuses` `read` or `write`, plus `metadata:read`. Merge-group membership cross-check stays a read: `pull_requests:read`, `contents:read`, `metadata:read`. No `pull_requests:write`. No `contents:write`. |
| Queue lane as well (`AEON_DELIVERY_ADMISSION_QUEUE=true`) | Everything above, plus enqueue and dequeue: `pull_requests:write`, `contents:write`, `metadata:read`. |

A merge-group webhook cross-checks synthetic commit ancestry with the live
GitHub queue prefix (read again after REST resolution), resolves every preceding
PR head, and stores
immutable membership with each binding revision, and re-evaluates every member
before posting success. Unknown membership posts failure. Queue-entry sightings
also retain synthetic SHAs; their partial membership can invalidate a group but
cannot grant success. A fresh webhook supplies complete membership. Observations
collected while the worker is disabled require a fresh resolved event before they can authorize a
group. Multiple PRs sharing a head retain all their bindings under the SHA key.

Holds, approval replacement/revocation, retargets and scope changes invalidate
live groups transactionally. Separate publisher and enforcement lanes post failure
and dequeue denied entries, so a stalled status request cannot starve queue removal. The admission response separates policy intent from GitHub evidence:
`enforcement=pending` remains until a fresh read confirms removal;
`effective=true` requires both denied admission and confirmed removal. Posting a
failure commit status alone never establishes effective enforcement. `merged_before_hold`
and its Desk notice are only for a denial that was already stored while the pull was
still open, when the merge wins during or after the dequeue. A merge or close that is
already visible on the pre-mutation read is terminal `superseded`, with no notice.

The outbox key is `(tenant, repository, SHA, context)`. A PostgreSQL session lock
serializes all publishers for the repository/SHA/context across processes. The
publisher reads current admission after taking the lock, holds the lock through
HTTP, and rechecks the revision before acknowledging. A hold can commit while a
POST stalls because no tenant transaction spans that POST. Uncertain outcomes
are durable; the owner reads GitHub and reposts current state before releasing
the lock. Persistent failures remain pending after bounded attempts. Restart
resumes pending rows. The 30s read is limited to active groups and the current
heads of open pulls. Merged heads and destroyed groups are retired and keep their
last confirmed state, so a merge does not paint `aeon/admission=failure` onto main.
Matching confirmed states avoid another POST,
preserving GitHub status limits. When the queue switch is on, the queue lane wakes from the dirty trigger.
A head that stays queued is read at most once a minute, the confirming read is
skipped when no enqueue or dequeue ran, and one installation token covers every
read in that pass. Timestamp-only observations and the publisher’s
own status webhook do not invalidate an acknowledged revision. That echo is
acknowledged before the observation lock and does not read GitHub. Every other
webhook kind tries the lock and, while reconciliation holds it, remains a
durable receipt for immediate retry instead of waiting. Unchanged P2
observations are seeded on startup.

Enqueue intent is committed before its external mutation, so a restart cannot
reset the retry cap. The final enqueue transaction re-evaluates current policy
under the tenant/tree fence, and holds that fence through the bounded mutation.
One same-head retry is allowed after a verified infrastructure failure on a
synthetic group. Replayed checks from that group do not buy another retry.
Required-code failures, repeated infrastructure failures and unconfirmed queue
ejections quarantine the head, surface `queue_quarantined` in admission, and
create one held Desk notice. Policy removal
is separate: lifting a hold resumes the same approved head without spending an
infrastructure retry.

| Rule / risk | Behaviour test |
| --- | --- |
| A stalled success cannot outlive current failure; replicas cannot post concurrently | `TestAdmissionPublisherSerializesStalledSuccessAndCurrentFailure` |
| Restart retains uncertain intent and publishes the current revision | `TestAdmissionPublisherResumesUncertainPublicationAfterRestart` |
| Timeout/5xx requires a read and current-state repost before acknowledgement | `TestAdmissionPublisherRepostsAfterUncertainOutcome` |
| Every member, including preceding entries, is checked; hold/revocation/retarget/scope fail every group; effective waits for removal | `TestAdmissionGroupsValidatePrecedingEntriesAndInvalidateEveryGroup` |
| Delayed green read cannot override newer red CI, hold or head facts | `TestAdmissionQueueRejectsStaleGreenReadAfterCurrentFactsChange` |
| Exact-head enqueue, duplicate sweeps, policy resumption, one verified infra retry, quarantine/Desk notice, lost merge race | `TestAdmissionQueueBoundsInfrastructureRetryAndReportsMergedBeforeHold` |
| An ordinary merge is not reported as a lost hold | `TestAdmissionPlainMergeDoesNotReportLostHold` |
| A steady queued head is not re-read on every pass | `TestAdmissionSteadyQueuedEntryBoundsReads` |
| Merged heads and destroyed groups are not republished | `TestAdmissionReconcileLeavesMergedSHAUnchanged` |
| Unchanged P2 observations seed intent after upgrade; partial queue sightings retain group identity without authorizing it | `TestAdmissionBackfillRetainsPartialQueueGroupsWithoutAuthorizingThem` |
| Unresolved membership fails closed and stays retryable | `TestAdmissionUnknownGroupMembershipFailsClosed` |
| App pins head/context/repository and rejects wrong grants, changed heads and GraphQL errors | `TestAdmissionAppPinsHeadContextAndRepository` |
| Queue switch off: harness enqueue, merge group, hold during the build, then merge posts failure on the group SHA and makes no queue call or write scope beyond statuses | `TestAdmissionStatusOnlyPublishesHoldOnGroupWithoutQueue` |
| Queue switch on selects the P3 queue lane; off selects status only | `TestAdmissionQueueSwitchSelectsLane` |

All publisher interleavings use barriers or cancellation, without sleeps.
Migration 1359 adds two tenant-isolated tables, nullable retirement and queue
check time, and nullable group identity on existing queue-failure evidence; the
DSAR inventory classifies every new field.
