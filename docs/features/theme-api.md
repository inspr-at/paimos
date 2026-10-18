# Theme API

Settings → Theme (AEON-642) lists the workspace default, shared workspace
themes and personal themes. Choose, duplicate, rename in place or confirm a
deletion on the page. New theme copies the current workspace default. Members
duplicate workspace themes to edit their own; managers with `settings.manage`
can edit shared themes, including the workspace default (`scope: default`).
Fresh selection or reload resolves a prior conflict; a conflict on another
theme does not disable the selected theme's editor. Loading more themes keeps
unresolved conflict feedback and recovery available until fresh active state loads.
Recovery belongs to each theme: another theme's conflict or successful deletion
cannot unlock an unresolved selected theme, even after pagination or a failed reload.
Pagination sits above the scrolling list and retains its space when the last
page arrives, keeping New theme, recovery and Colours controls in place.
Colours has separate light and dark accents, optional derived dark values,
presets, a native picker and hex input, plus recurring marker choices. The light
and dark previews update while editing; the rest of the page keeps its saved
appearance. Contrast below 4.5:1 warns without blocking Save. Suggest adjusts
only the failing mode to a readable shade of the same hue. Discard and Save
appear in an overlay bar, preserving the page layout. Saves retain the Agents
configuration and use the existing revision checks; conflicted drafts must be
discarded and refreshed before saving again.
The Agents card shares the theme editor's saved selection and revision. The app
waits for saved mode and active theme before mounting its first authenticated
view. The approved OKLCH engine supplies accent roles, derived dark values,
contrast-safe text and marker colours; a single identity-bound runtime stylesheet
applies confirmed edits and selections. Its bounded first-paint cache retains only
the owner and derived CSS. Drafts remain isolated in previews. Filled agent marks
use the higher-contrast white or dark ink for their actual palette colour, including
Custom; recurring badges use the same ink rule and a 12 px heavier glyph.
Personal heartbeat writes preserve the stored legacy appearance for rollback.

AEON-641 provides theme data for the appearance consumers. `GET /api/themes`
returns visible workspace themes and the person's own themes with UUID keyset
pagination (`after`, `limit`, maximum 100). `GET /api/me/theme` returns the active
record, default ID, selection revision and any deletion notice. Agents may read
the workspace default with `profile.read`; theme writes belong to people.

Create with `POST /api/themes`, duplicate with `POST /api/themes/{id}/duplicate`,
rename or replace values with `PATCH /api/themes/{id}`, and delete with
`DELETE /api/themes/{id}?revision=N`. Updates, copies and deletes require the
record's revision; `PUT /api/me/theme` requires the independent selection
generation in `revision` (initially 0) and a `theme_id` or null to follow the
default. Return the generation unchanged; it is an opaque CAS value, not a
counter to increment. The first explicit default is saved and audited. Stale
revisions return 409. Personal writes require the owner and `profile.write`
(or the portal equivalent); workspace/default writes require `settings.manage`.
Personal records and their events remain owner-only, including for managers.
The full schemas are in [the API contract](../../api/openapi.yaml).

Linking people preserves theme owners and choice rows. Linked identities can
read, edit and undo their shared personal themes; unlink restores each original
owner's privacy. The canonical person's saved choice wins (including an explicit
default); otherwise the alias choice with the lowest principal UUID wins. Choice
edits update that winning row and keep other rows for unlink. If unlink makes a
chosen theme private, the API returns the default without that theme's ID/name,
retaining the selection generation for the next choice. Changing the winning
identity changes that generation, even when both physical row revisions match.
Undo restores an unsaved preference separately from an explicit default, so
alias inheritance resumes. Its retained row carries a fresh generation to
reject stale writes, including revision 0; a subsequent explicit default is
saved and audited again. The migration preserves physical row revisions
and existing audit snapshots; audit remains append-only and its audience follows
current links.

Each tenant starts with Porcelain, using the shipped light/dark accents and
native agent artwork settings. Null accent dark means derived by the consumer;
null agent ring/size means the chosen style's drawn default. Deletion is
reversible through the event undo API. A selected tombstone resolves to the
workspace default and supplies `fallback_notice` until another choice is saved.
Undo restores availability without replacing later choices. The default itself
cannot be deleted. This package supplies the API; Settings and applying its
values are delivered by AEON-642–644.
