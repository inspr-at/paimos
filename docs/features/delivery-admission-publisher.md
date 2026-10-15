<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Merge-queue admission publisher

P3 connects the P2 admission evaluator to explicit GitHub queue operations and
one durable `aeon/admission` publisher. It ships disabled. The host setting
`AEON_DELIVERY_ADMISSION_ENABLED=true` enables the worker for the repository and
tenant already bound by `AEON_REVIEW_APP_*`. OPS provisions the App and configures
its private key path; the code reuses the existing configured-path reader. No
App provisioning, ruleset cutover, bypass grant or deployment is part of P3.

The App adapter accepts only admission statuses and explicit enqueue/dequeue.
Enqueue sends `enqueuePullRequest(expectedHeadOid)` with the admitted PR head.
The adapter requests repository-limited installation tokens, checks the granted
repository and exact permission set, and revokes tokens after use. Queue calls
request `pull_requests:write`, `contents:write`, and `metadata:read`; status calls
request `statuses:read/write` and `metadata:read`. The existing observation
channel keeps its independent read-only token. No production GitHub writes run
in tests.

A merge-group webhook cross-checks synthetic commit ancestry with the live
GitHub queue prefix (read again after REST resolution), resolves every preceding
PR head, and stores
immutable membership with each binding revision, and re-evaluates every member
before posting success. Unknown membership posts failure. Queue-entry sightings
also retain synthetic SHAs; their partial membership can invalidate a group but
cannot grant success. A fresh webhook supplies complete membership.

Holds, approval replacement/revocation, retargets and scope changes invalidate
live groups transactionally. Separate publisher and enforcement lanes post failure and dequeue denied
entries, so a stalled status request cannot starve queue removal. The admission response separates policy intent from GitHub evidence:
`enforcement=pending` remains until a fresh read confirms removal;
`effective=true` requires both denied admission and confirmed removal. Posting a
failure commit status alone never establishes effective enforcement. A confirmed
merge that won the race is reported as `merged_before_hold`, with a Desk notice.

The outbox key is `(tenant, repository, SHA, context)`. A PostgreSQL session lock
serializes all publishers for the repository/SHA/context across processes. The
publisher reads current admission after taking the lock, holds the lock through
HTTP, and rechecks the revision before acknowledging. A hold can commit while a
POST stalls because no tenant transaction spans that POST. Uncertain outcomes
are durable; the owner reads GitHub and reposts current state before releasing
the lock. Persistent failures remain pending after bounded attempts. Restart
resumes pending rows; confirmed rows are periodically reconciled to repair a
late obsolete HTTP outcome. Unchanged P2 observations are seeded on startup.

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
| Exact-head enqueue, duplicate sweeps, policy resumption, one verified infra retry, quarantine/Desk notice, lost merge race | `TestAdmissionQueueBoundsInfrastructureRetryAndReportsMergedBeforeHold` |
| Unchanged P2 observations seed intent after upgrade; partial queue sightings retain group identity without authorizing it | `TestAdmissionBackfillRetainsPartialQueueGroupsWithoutAuthorizingThem` |
| Unresolved membership fails closed and stays retryable | `TestAdmissionUnknownGroupMembershipFailsClosed` |
| App pins head/context/repository and rejects wrong grants, changed heads and GraphQL errors | `TestAdmissionAppPinsHeadContextAndRepository` |

All publisher interleavings use barriers or cancellation, without sleeps.
Migration 1359 adds two tenant-isolated tables and nullable group identity on
existing queue-failure evidence; the DSAR inventory classifies every new field.
