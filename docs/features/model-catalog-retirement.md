# Sourced model retirement

The registry and the provider catalog expose a lifecycle object with state,
date, successor, reason and source. Missing lifecycle evidence stays null.
Native provider or harness successor facts take precedence over aggregators;
otherwise `aeon_model_line` and `aeon_model_version_newer` identify a newer
sourced numeric version of the same provider line. Floating aliases and other
non-numeric versions never establish an inferred successor, even with harness
evidence. Deprecated, retiring and vanished states retain their
source even when a successor is known.

`GET /api/models` keeps its complete profile list, including retired profiles.
Lifecycle enrichment uses only those identities and their source model lines,
after the authorized profile-read transaction releases the tenant fence.
Numeric successor comparisons run in one query. If enrichment exceeds its
two-second deadline, evidence budget or another read fails, profiles remain
available with `lifecycle: null`; optional display evidence never rejects an
otherwise successful profile read.

Only a complete successful provider refresh records presence or disappearance.
An unavailable, revoked or incomplete source keeps the previous evidence.
A later complete listing can restore presence. Package B owns scheduled harness
probes and their native lifecycle facts; this package consumes their fact shape.

`GET /api/models/outdated` returns a keyset page (`limit` 1–50, `cursor`) of
registered versions with `uses[{label,who,from,to}]` and a preview `revision`.
Default, Except for, fallback, never and lock references, plus retained legacy
pinned cells and role routes, are included. Stable line orders already selecting
a newer active version are not counted as references to an older pin. Private
person or project uses are withheld from readers without authority, with
`incomplete: true`. Bounds reject an oversized preview rather than silently
omitting references. Account daily caps remain on accounts and do not move.

`POST /api/models/outdated/retire` takes `{versions,revision}`. Each version is a
canonical `provider:model_id` identity. A person with `models.manage` can retire
up to 50 versions atomically. Moving preferences additionally requires workspace
`model_prefs.manage` and current project authority. The write checks permissions
under the tenant/catalog fences and rejects stale previews, unknown versions,
selected successors, conflicting rules or role holds, and used versions without
a registered successor on their existing harness. Equal role policies merge at
their earliest priority. It reuses the line-reference
transfer and account successor policy without widening account grants. It
returns `{moved,removed,undo_token}`. Existing runs retain their immutable pins.
Consumers retain the preview revision with the selected version IDs, display
rejected writes as errors, and reload the catalog and preview after a successful
retirement or Undo.

`POST /api/models/outdated/undo` takes `{undo_token}` and requires current
`models.manage`, `events.undo`, the corresponding preference/project authority,
and `events.undo_other` for a different actor. The opaque token addresses one
retirement event and is stored only as a hash. Undo restores the changed
references and previous retirement schedules in one transaction. Revision
numbers increase. Later edits to those references or a repeated Undo return 409;
unrelated references are preserved. One compensating event names the original.

No migration is required. These endpoints use the existing tenant-scoped facts,
retirement rows and append-only events. Recorded source fixtures and controlled
lifecycle/version vectors test precedence, unknown evidence, successful batch
moves, Undo, stale writes, privacy, event-failure rollback and fenced revocation.
