# Run person binding

New executable agent runs retain the canonical person they act for in
`agent_runs.acting_person_id`. The server derives that person from existing,
verified provenance; a caller cannot select another run's person through a
header. The write-once seam is `agentruns.SetActingPersonTx`, backed by the
shared transaction implementation in `internal/workqueue/run_person.go`.

| Creation path | Authoritative person |
| --- | --- |
| Personal start or explicit person-bound API request | Signed-in canonical person or the API key's explicit person binding |
| Routine queue/dispatch | Saved routine definition owner |
| Lead runtime | Verified lead policy owner |
| Review or fix | Person recorded on the originating run |
| Child | Person on the parent run resolved from the existing worker lease |
| Recovery | Person recorded on the original run |
| Vendor handoff | Person recorded on the original run |
| Native intent claim | Intent's `owner_person_id`, checked against the run binding |

An external manual review without an author run uses the requesting person's
explicit identity. Key issuers, pairing approvers, machine owners and
`prefs_person_id` never supply a missing person. Unresolved admission returns
`run_person_unresolved` and persists a value-free `run.admission_refused` audit
after rolling back partial resources. Each successful binding emits one
`run.person_bound` event containing run ID, person ID and source rule; an
unchanged replay emits no second binding event.

The final claim transaction rechecks that the person is active, can still see
the project and may start on the claiming computer, under the existing access
fences. Revocation returns `permission_revoked`. Existing runs with a NULL
person retain their previous claim behavior until the separately scoped S1-07
cut-over. This feature adds no migration or launch permission.

Stopping and yielding remain possible after the run person's deactivation or
project-access revocation. The existing authenticated worker transport has a
closed safety allowlist: yield, stop, stop-control completion and confirmed
exit. Each operation still requires a live transport key and the exact target
session's agent and lease; process controls retain their ownership, generation
and expiry fences. The fallback returns only session/control metadata and
audits the requesting actor. It omits task content, steer text, settings and
handovers, and cannot admit input, export, recovery or another start. Ordinary
authorized responses keep their existing projections.

Behavior checks cover source selection and write-once binding, unresolved
manual/scheduled routine and review rollback audits, final-claim revocation,
and authenticated safety controls after person deactivation or access loss.
