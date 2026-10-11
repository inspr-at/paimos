# Project settings

The project’s Settings tab is its home for project settings. `/p/KEY/settings`
opens General; `/settings/delivery`, `/settings/agents`, `/settings/access` and
`/settings/routines` under that project select the other sections.

General saves the name and the full description through revision-checked node
updates. Imported description fields are kept consistent with the shared header.
Shared groups use `/project-groups/assign`. Confirmed changes offer Undo; a newer
project revision prevents Undo from overwriting later work. Field Undo changes
only its own field, and lifecycle Undo preserves both field drafts, including
drafts retained after failed saves. The key stays read-only.

Project admins with `nodes.write` can freeze, unfreeze, archive or restore the
project. Delete retains the approved sheet, but its final action is disabled and
the sheet explains that deletion is unavailable. A state patch cannot remove a
project and its tickets; enabling deletion requires an authorized, revision-checked
server operation. Keyboard and form submissions also refuse deletion.
Freeze, Archive and their reverse actions use the revision shown when the action
starts; the server remains the authority.
Freeze and Archive enforcement across agents, routines and delivery is owned by
AEON-1234. This UI does not grant a permission or bypass a server restriction.

“How this works” uses the approved AEON-1227 explainer map. Its computer example
uses a tenant-data placeholder to keep operator hosts out of public sources. It focuses
its heading, overlays the page, returns focus on Escape and uses a bottom sheet
on phones. Escape leaves a field before closing its overlay; Command+Enter on
macOS or Ctrl+Enter elsewhere submits through the shared shortcut registry.

Agents reuses the Work context card and links to the existing workspace owners.
Routines reuses the existing routine card, including selected routine and consent
queries. Access links to Settings › Access › Projects until AEON-1237 moves the
editor.

Repository & delivery links one GitHub repository and its workflow files, with
7, 30, 60 or 90 days of history. AEON-1234 owns the server job: this page makes
one start/resume request and polls the source, never loops the backfill cursor.
Saved Actions permission failures name the GitHub owner and the configuration
path; Check again immediately retries the current history step, bypassing its
retry time while keeping progress. The repository menu can restart history.
Missing events, stale events, GitHub failures and partial history stay explicit.
Phase and count details appear only when a backfill response supplies them; the
source read has no history-progress fields. Every ordinary state reserves the
approved repository slot; only the link form may grow.

Checks and deadlines read effective values and sparse project values separately
from `/delivery-settings` and `?view=overrides`. Reset removes just that saved
value, preserving inheritance and unrelated overrides. An ETag with If-Match
rejects edits based on an older revision; the existing response schema and
unconditional API callers remain compatible. Failed writes preserve drafts.
Background revalidation preserves a dirty draft’s original revision; accepting
new saved values requires an explicit reload before that draft can overwrite them.
`delivery.read` permits viewing; `delivery.manage` permits these mutations.

Delivery’s empty banners and problem chips, the unlinked ticket delivery slot,
the lead launch reason and each Projects row menu link to the settings owner.

Legacy `#project-work-context` selects Agents. `#recurring-work`, `?recurrence=`
and `?consent=1` select Routines while retaining their hash and query. The header
edit button opens General and focuses Name. Other project tabs keep their routes.
