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

An ordinary 401 or 403 pauses automatic GitHub reads. The existing Delivery
notice path sends one permission notice per receipt to the currently authorized
project lead or coordinator session. Receipt scheduling and notice acceptance
commit together. A missing or unauthorized lead leaves notice routing pending;
later due passes retry routing without reading GitHub. After installation access
is repaired, redelivering the same signed GitHub event resumes its unfinished
step. Its delivery ID, repository, installation and payload digest must still
match, and completed checkpoints remain intact. A permission refusal on the last
ordinary attempt retains one explicit repair attempt.

A 429, or a 403 carrying Retry-After or the exhausted rate-limit marker, waits
for the parsed Retry-After/reset hint. Scheduling is bounded to one minute through
one hour, with a one-minute fallback for absent or invalid hints. These waits do
not consume the twelve-attempt budget. A duplicate webhook cannot bypass the
stored rate-limit deadline. Header text is discarded; only the derived due time
is persisted. Best-effort token revocation cannot replace the failed read's hint.

404, server and network failures retain bounded retries. At exhaustion, the
receipt keeps the upstream class and the failed step's `gave_up` checkpoint;
it does not report successful completion or restart reads on redelivery.
