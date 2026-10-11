# Policies

Settings → Policies (`/settings/policies`) explains existing rules. Each tab
follows its source permission: review ladders need `models.read`; agent key
limits read the permission registry with `roles.read` and a person session;
ownership and advisory rules are visible to signed-in people. Links lead only to
existing screens the person may open.

Role ladders moved to Settings › Models (AEON-1204, with the Models v2 server
migration AEON-1158). The Review ladders tab shows one line, "Role ladders moved
to Settings › Models", with a link to `/settings/models`. It reads and writes
nothing: Policies never calls `GET` or `PUT /api/models/routes`. The server
answers that `PUT` with 410 Gone and keeps the `GET` only as a deprecated
compatibility read. The tab has no role selector, ladder list or editor, and a
person without `models.read` sees its permission sentence instead. Under Rules
elsewhere, the Role ladders row links to the Models page.

Model preferences use Default, You and an explicitly visible Project, with the
source GET/write permissions. Project-only management also needs workspace
model visibility to use this screen. Each mutation carries the GET's canonical
`If-Prefs-Person` and the addressed level revision. Row edits preserve both
complexity buckets and the row lock; Reset affects only that selected row.
Row DELETE sends the captured revision in the query string, alongside project
context where needed; Undo of a newly added row uses the same conditional DELETE.
Provider and section-lock writes send only the three scalar fields, never
`rows`; neither Save nor Undo issues whole-level DELETE. Archived and sibling
work-kind settings therefore remain stored by these UI operations.

Save is provisional until the source confirms its actual result. Undo is a
conditional compensating source write with the confirmed token/revision, not an
event deletion. Newer changes, revoked permissions, linked-person changes and retired
kinds refuse honestly without retrying the old draft. Unknown outcomes require
reconciliation; an equal value alone never certifies success. Provider Undo does
not lower requirements already stamped on existing runs. Navigation discards
local drafts and Undo; it does not promise to roll back an in-flight server write.
For preferences, a focus permission refresh keeps drafts, confirmed Undo and
pending-write handling when authority remains unchanged. Identity and relevant
permission changes invalidate them. Local work-kind and editor-mode changes
discard mutations while retaining the same source document during the next
preview read, so picker options and preference rows remain on screen.

Queue routes finish buffering request bodies before opening their transaction
or retaining tenant/tree/pairing locks. Input is capped at 1 MiB and shares a
30-second request deadline with the transaction; interrupted input returns 408,
and oversized input returns 413 before queue work starts.

Editors reserve useful read/help space above the previews and keep long content
inside scrolling bodies. Phone editors use full-height sheets with safe-area
action bars. Save keycaps follow the platform; keyboard Save focuses the stable
Save action before fields are disabled, retaining focus for subsequent keyboard
Undo and Escape. Escape first leaves a field, then
closes the editor. Browser geometry for the Policies tabs lives in
`web/tests/stability.spec.ts`, and `web/tests/policies-ladders-moved.unit.test.ts`
guards the moved line and the absence of ladder requests. Screenshot evidence
is generated locally under `web/test-results/` and is not committed.
