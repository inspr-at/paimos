# Policies

Settings → Policies (`/settings/policies`) explains existing rules and provides
scoped editors for saved job orders and model preferences. Each tab follows its source permission: review ladders need
`models.read`; agent key limits read the permission registry with `roles.read`
and a person session; ownership and advisory rules are visible to signed-in
people. Links lead only to existing screens the person may open.

`GET /api/models/routes?role=review-gate` reads one of the six model roles,
ordered by priority and profile id, with at most 50 steps and an explicit
`truncated` flag. An unseeded registry returns `setup: false` without creating
profiles or events. The read applies a five-second statement timeout and returns
503 with `Retry-After` if it expires. CLI priority and qualified review dispatch
are labelled separately; this display endpoint does not resolve a dispatch.
Review family ordering and qualification floors are shown only for Review gate.
Suspension expiry includes an explicit UTC time and date. A late server refusal
clears the private rows while keeping the existing selector and detail link in
place; a person without the source permission sees only its permission sentence.

People with `models.read` and `models.manage` can edit one complete role order,
using its quoted `If-Match` token. Truncated snapshots remain read-only.
Add, move and remove operations renumber the draft in displayed order, including
orders with gaps in their stored priorities. Ordinary managed reviews retain
built-in family/tier fallback until a person deliberately saves the review order.
The draft starts from the currently applied fallback. Save sends
`order_mode=saved` on the conditional `review-gate` PUT; Undo restores the captured
rows and `legacy`/`saved` mode together. Omitted mode and whole-tenant legacy PUTs
preserve activation. The strong role token covers both rows and mode; mode-only
changes emit `model.routes_replaced` with unchanged before/after arrays and
`before_order_mode`/`after_order_mode` metadata. GET returns `order_mode` and
`managed_fallback_order` from the same snapshot, and review traces capture the
mode. Preferences may still choose another qualified reviewer. Author-family,
effort/tier, platform, account and residency checks stay enforced; security uses
its separate refusing path. Already queued profile/account bindings stay fixed.

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
event deletion. Ladder Undo uses `expiry_policy=clear`; expired holds stay
available. Newer changes, revoked permissions, linked-person changes and retired
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
closes the editor. Browser geometry and request-contract coverage lives in
`web/tests/policy-editors.spec.ts`; screenshot evidence is generated locally in
`web/test-results/aeon-633web/` and is not committed.
