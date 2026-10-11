# Routines: versioned templates

Routines extend recurring work with a scoped person owner and an optional saved
agent assignment. Existing ticket-only recurrences keep their behaviour. The
routines release ships with automatic execution off; project enablement is a
separate person-owned decision after coordinator acceptance and OPS qualification.
Saving, previewing or applying a template never starts an agent.

Three immutable version `1` templates are available through the API and CLI.
Each includes English and German names, goals, instructions, criteria and release
copy. German copy is impersonal. Generated work is hidden from release notes and
retains the normal Done copy checks. All definitions are project-scoped, owned by
a supplied canonical person administrator, and output ordinary work leaves with
the existing routine provenance. No project, parent, owner or work-kind UUID is
embedded in the catalogue.

| Template ID | Default trigger | Registry role | Native requirement |
| --- | --- | --- | --- |
| `weekly-cleanup` | Monday 09:00, Europe/Vienna | `build` | Qualified native coding host |
| `test-review` | Tuesday 09:00, Europe/Vienna | `build-hard` | Qualified native coding host |
| `website-release-update` | Each publication in one supplied source project | `build-hard` | Qualified native browser host |

Weekly cleanup inspects current repository health, deduplicates evidence-backed
findings and performs already permitted reversible cleanup. Those local changes
need no additional human gate. Critical changes still follow the effective
policy, and coordinator different-family review, CI and repository/person
shipping gates remain. Security paths retain their stronger existing route.
Outputs can include a bounded knowledge report, local commit or draft PR; no-change
runs report that result honestly.

Test review maps actual risks against current manifests, `agent-test-policy` and
the accepted test-tier decisions. Supply the current policy knowledge references
in `sources`. Proposals identify the retained risk and its existing guard. Test
removal, reclassification, skipping or weakened assertions require an accepted
person-approved note for the specific change. Current manifests and policy stay
in force until acceptance. PR and pipeline requests cover approved small changes
only. The routine cannot approve itself or edit its own policy; v1 has no learning.

The website example is installed explicitly in the INSPR definition/output
project under the supplied INSPR-521 parent, with its responsible owner/person.
The supplied AEON release source must be a different project in the same tenant.
Provide accepted site-process knowledge references for INSPR-541/RUN-12 and
GUI-22. The template sets `filter.project_ids` to that one source. The existing
release subscription retains pending publications during overlap and uses the
same per-publication identity for scheduled and manual release choices. Template
installation leaves the companion INSPR-541 weekly audit untouched.

Website work inspects the exact publication, codename, canonical version and
approved release copy against live sites and source at the deployed SHA. Role
`scout` handles extraction; `build-hard` handles judgement; the coordinator owns
`review-gate`. Codex builds, while the qualified native Claude capture broker
handles bounded, read-only Chrome captures outside the Codex sandbox. No unsafe
host fallback is granted by a template. URL/claim deduplication, source identity,
per-site findings, screenshots and the draft PR are recorded against the run.
Mechanical version-pin and evidence-backed planned-to-live corrections remain
drafts. The target/head-bound owner's Decision Desk GO and OPS deployment/live
check are required. The routine never deploys and never treats a merged draft as
live evidence. Existing house vocabulary and retired-feature boundaries apply.

Discovery and preview have no installation side effects:

```sh
aeon recur templates list --json
aeon recur templates preview weekly-cleanup 1 --body-file routine-config.json
```

An explicit configuration supplies tenant references; the placeholders below
must be replaced with authorized UUIDs:

```json
{
  "project_id": "<output-project UUID>",
  "parent_id": "<output-parent UUID>",
  "owner_principal_id": "<canonical administrator person UUID>",
  "work_kind_id": "<active work-kind UUID>",
  "language": "en",
  "sources": [{"kind": "knowledge", "id": "<accepted policy UUID>"}]
}
```

`language` defaults to `en`; `de` selects the complete German work copy, with both
languages' release copy retained. `tags` accepts existing target-project or
workspace tag UUIDs. Cleanup and test review accept an optional ordinary `time`
`trigger` override. The website template requires `source_project_id` and at
least one knowledge source, and rejects trigger overrides. Both the saving
caller and canonical owner need live target/source permissions. Work kind and
parent validity use ordinary recurrence validation.

Preview returns `input` and the ordinary calendar/execution-policy `preview`.
The `input` can also be passed to `POST /api/recurrences/preview` for validation.
Time triggers show four dates; release event times cannot be predicted.

```sh
aeon recur templates apply weekly-cleanup 1 --body-file routine-config.json
```

Apply creates a **paused, unconsented** ordinary definition with `queue_each`
false, overlap `skip`, catch-up `one` and budget `off`. A supplied `budget` may
instead request `tokens`, `money` or `both` with the ordinary positive ceilings;
both stops at the first applicable ceiling. Budget off does not waive admission,
recovery, authorization or native qualification. Unsupported budget/runtime
routes remain unavailable; stored intent is not qualification evidence.

The closed outward catalogue is `work.create`, `work.update`, `knowledge.write`,
`pr.open` and `pipeline.request`. Every effect still uses current permission and
policy checks. Secret access, own-rule/gate edits, force push and automatic
deployment remain prohibited. Installation grants no project enablement,
execution identity or execute consent; those require the existing separate
person/OPS flow.

`GET /api/recurrences/templates` lists the catalogue.
`POST /api/recurrences/templates/{templateId}/{templateVersion}/preview` renders
and validates a configuration.
`POST /api/recurrences/templates/{templateId}/{templateVersion}/apply` explicitly
installs it. Unknown IDs/versions return 404; invalid configuration returns 400.
Discovery uses `nodes.read` for people and an explicit `recurrences.manage` key
scope and grant for agents. Preview and apply require `recurrences.manage` in the
supplied output project; owner and source permissions are checked separately.

Apply returns 201 for creation and 200 for an existing matching configuration.
The identity binds the tenant, pinned template version and normalized definition.
UUID case and source/tag order are canonicalized. An omitted calendar start date
is excluded from retry identity, so retrying on a later day returns the original
schedule. Concurrent retries and lost responses create one schedule and one
creation event. Reapply returns the **current** record and preserves local edits,
pause state and consent. A retired identity returns 409. Changing configuration
creates a distinct definition; use ordinary revisioned updates to edit an
installation. Retain the same configuration file for retries. Plain
`POST /api/recurrences` remains ordinary creation and does not gain template apply
idempotency.

Published template versions stay immutable. New copy or behaviour requires a new
version while retaining old versions for reproducible preview and retries. No
migration, automatic installation or background configuration is required.
