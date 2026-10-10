# Project lead settings (backend groundwork)

`aeon project lead-settings get PROJECT` reads the lead policy. Use `set
PROJECT --revision N --from FILE` with a sparse override JSON object, or `reset
PROJECT --revision N` to inherit again. `--workspace` selects execution defaults
instead of a project. Pass `--session-cookie-file FILE` to use an existing
person session through the
CLI's bounded session transport, without bearer authentication or storing the
session. Otherwise these commands use the configured agent caller, and the
server refuses its edits. Edits require an active person with
`model_prefs.manage`. A project's first edit records that
person as owner; subsequent edits require that same canonical person. Reset
retains the owner and advances the revision, preventing stale saves and takeover.

The additive API is `/api/projects/{projectId}/lead-settings` and
`/api/settings/lead-policy` (GET/PUT/DELETE). PUT replaces the sparse `overrides`
object with an expected `revision`; DELETE takes the revision as a query value.
Null/omitted selectors inherit; empty host/account arrays allow no targets.
Project host/account filters intersect workspace filters. Selected IDs must
belong to the editing person; filtering never supplies pairing, spending or
execution consent. Project readers see redacted explanations; private selectors
are visible only to the owning person (workspace selectors to their editor),
and audit events store revisions without account/host identifiers.

Model settings reference the existing work kind and normal/complex bucket;
Default → You → Project model preferences and locks continue to resolve live,
with their revision vector returned to the owner. Recovery limits share a finite
attempt and agent-hour budget across build/review/fix; zero disables recovery
by default, and a project cannot increase workspace ceilings. The existing dial
remains `/api/agents/plan`; there is no new concurrency control.

This policy does not launch work. `automatic_launch_enabled` remains false
pending AEON-603 acceptance. The lead lifecycle/claim/start consumers must
recheck current owner authority and policy revisions, then dial, harness,
account room and host load under their final transaction fences. Unreadable
mandatory gates mean wait. No model route, saved setting or queued work grants
merge, deploy, credential rotation, force-stop or attached-session consent.
AEON-734 owns lifecycle/ownership succession, AEON-598–600 execution, AEON-731
recovery and AEON-741 the approved UI.

Qualified routine execution is a separate person-owned project setting,
documented in [Routine execution consent](routine-execution-consent.md). The
lead lifecycle projection now resolves that qualification; this policy editor
continues to grant no execution authority.
