<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Delivery receipt errors

Verified GitHub webhooks retain their unfinished step in
`delivery_webhook_receipts.last_step`. The existing `last_error_class` field and
webhook failure logs distinguish `http_401`, `http_403`, `http_404`, `http_429`,
`secondary_limit`, `http_5xx`, and `network`. Other failures retain their existing
classes. Diagnostics never store upstream response bodies, header values,
credentials, or request queries. Successful completion clears the last error.
Previously unfinished `github_http` receipts continue from their stored
checkpoints and receive a specific class on their next failed read. Existing
terminal receipts are not reopened.

An ordinary 401 or 403 pauses automatic GitHub reads. The pause epoch is the
time of the first pause for that tenant, repository, installation and class, and
it is cleared when a probe shows reads work again. The existing Delivery notice
path sends one permission notice per tenant, repository, installation, class and
pause epoch to the currently authorized project lead or coordinator session. The
notice text includes how many receipts are affected. A later separate outage uses
a new epoch and notifies again. Receipt scheduling and notice acceptance commit
together. A missing or unauthorized lead leaves notice routing pending; later due
passes retry routing without reading GitHub. The hourly pass makes one bounded
probe read per installation. That probe does not count as an attempt and is not
a read per receipt. When it succeeds, every paused receipt for that installation
becomes due immediately, the pause class and epoch are cleared, and the change
is audited. Redelivering one verified webhook still resumes that delivery. Its
delivery ID, repository, installation and payload digest must still match, and
completed checkpoints remain intact. A permission refusal on the last ordinary
attempt retains one explicit repair attempt.

A 429, or a 403 carrying Retry-After, the exhausted rate-limit marker, or
GitHub's secondary-rate-limit marker in a size-limited body slice, waits for the
parsed Retry-After/reset hint. The body text is not stored; only the derived
class is kept. A confirmed refusal, with neither those headers nor that marker,
pauses as permission. Scheduling is bounded to one minute through one hour, with
a one-minute fallback for absent or invalid hints. These waits do not consume
the twelve-attempt budget. A duplicate webhook cannot bypass the stored
rate-limit deadline. Header text is discarded; only the derived due time is
persisted. Best-effort token revocation cannot replace the failed read's hint.

404, server and network failures retain bounded retries. At exhaustion, the
receipt keeps the upstream class and the failed step's `gave_up` checkpoint;
it does not report successful completion or restart reads on redelivery.
