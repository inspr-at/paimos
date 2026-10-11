<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Delivery history and project lifecycle

Linking a delivery metric source schedules history reading on the server. Changing
its repository or workflows starts a new history window (1–90 days, default 30).
The existing backfill endpoint still returns one bounded step and also schedules
continuation. `restart` replaces the history window. Progress survives leaving
the page and restarting the server; each step stores its cursor and facts together.

The source exposes `last_read_error` (reason, missing permission when known, and
check time), `retry_at`, and `last_event_at`. A GitHub 403 requesting `actions=read`
becomes `actions_read_missing`; a missing installation/repository becomes
`not_installed`; other read failures become `github_unavailable`. Permission and
installation failures retry after 30 minutes, other failed steps after five
minutes. Deferred job reads keep their existing retry hour. The server honours
these times. `POST /projects/{projectId}/delivery/metrics/check-again` bypasses the
retry time immediately without discarding progress; it requires `delivery.manage`.
A successful step clears its read error. Signed, installation-bound repository
webhooks update the last event time; clients consider seven days without an event
stale. A linked source without any webhook has a null event time.

The job stores only the initiating principal and authenticating key metadata IDs.
It reloads current authority and checks `delivery.manage` again under the tenant
fence after each GitHub read. Revocation prevents continuation. Source revisions
prevent an old request from overwriting a changed source. All progress and
error/event fields retain tenant/project row-level isolation.

Frozen and archived projects admit no new lead starts/dispatch, routine work or
queue claims/starts. The admission reason is `project_frozen` or
`project_archived`. Existing processes retain ownership and can report completion;
people can continue editing. Project deletion additionally requires a live
project Admin or workspace Owner/Admin role. A requested project key is reserved
within its tenant, including deleted projects. Creation returns 409 with the
visible holder's project name; hidden holders produce a generic reservation
conflict. Historical imported alias collisions remain readable.
