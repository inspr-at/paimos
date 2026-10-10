# Recently deleted views

`GET /api/views/deleted` lists only the authenticated principal's own deleted
saved views from the last 30 days. Another person's shared view is excluded.
Tenant and project visibility continue to apply through row-level security.
Each item contains `id`, `project_id`, `name`, `mode`, `deleted_at` and
`deletion_reason`.

Pages use `limit` (default 50, maximum 100) and an opaque `cursor`. Deletions
sort newest first, then by descending id to make ties deterministic.
`next_cursor` is null at the end. A cursor keeps the first page's 30-day time
window, belongs to its tenant and principal, and expires after one hour.
Restoring a cursor's anchor does not prevent continuation. The query has a
five-second deadline; a failed query returns an error without a partial page.

Reasons come from the latest visible `view.deleted` audit snapshots matching
the current deletion. Owner-authored evidence yields `owner_deleted`.
System-authored empty-column retirement snapshots from migration 1249 yield
`retired_by_upgrade`. Missing, stale, unreadable or ambiguous evidence yields
`unknown`; deletion time alone never determines the reason.

`POST /api/views/{viewId}/restore` remains the existing owner-only operation.
It preserves the id, settings and links and records `view.restored` in the same
transaction. Already restored views return a conflict. The restore-list UI is
a separate delivery awaiting its design round.
