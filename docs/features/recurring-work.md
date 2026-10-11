# Recurring work

Recurring tickets are shipped (AEON-573, [Coral Cargo](https://github.com/inspr-at/paimos/releases/tag/v261005070923.0.0))
and are the simplest routine: the scheduler creates a ticket.
[Routines (AEON-680) are planned](web-workspace.md#planned-work) to add saved
agent assignments, typed action gates, budgets and run logbooks. The recurring
ticket scheduler does not supply those capabilities or Flow 2.

In the web app, **Repeat…** in a ticket or epic's More menu (Shift+R while
its panel is focused) opens the recurrence editor. Project **Settings →
Recurring work** lists definitions with Pause/Resume, revision-checked Undo,
Run now, Edit, History, Copy CLI command and Delete. Read-only people can inspect
definitions and history; changing them needs `recurrences.manage`. The editor
previews unsaved schedules through `POST /api/recurrences/preview`, using the
server's calendar engine. Created tickets show the recurrence name and occurrence
number, linking back to project Settings. Array-based template criteria are
readable and editable in the ordinary ticket panel.

The additive UI contract stores the optional recurrence `name` in `template`.
`GET /api/recurrences/{id}/history` pages 50 events with a `before` cursor and
`all|created|skipped|changes` filter. `GET /api/recurrences/{id}/releases` lists
up to 100 published project releases, explicitly reports truncation, and includes
existing receipts. Names use supplied marketing codenames, with the canonical
version on hover. Manual release runs (`--release-key`) share their durable key
with automatic runs. `--force-overlap` is an explicit manual confirmation and
does not edit the overlap policy; `--revision` can bind Run now to a definition.
Delete requires `expected_revision`; it pauses the definition and appends a
retirement event. Receipts, the definition and created tickets are retained;
retired definitions are omitted from reads and cannot be resumed or run.
Project-only readers see recurrence history and retirement events for visible
projects. The scheduler ignores retired definitions even if an older client
resumed the row. Both overlap prevention and Run now's open-previous notice use
the node list's closed-state rule, including each kind's configured categories.

`aeon recur create|list|get|update|pause|resume|run-now|preview` manages
server-owned schedules. Create reads a JSON definition from stdin or
`--body-file`; update takes the recurrence UUID and a full definition with
`expected_revision`. Pause/resume accept `--revision`, or read the current
revision before writing. Run now requires `--idempotency-key` so retries return
the original occurrence receipt. Named instances and `--json` work as usual.
Create and update also accept repeatable `--tag NAME|UUID`, resolved in the
target project or workspace and added to the JSON template's tags.
`--description-file FILE` replaces the template description;
`--criteria-file FILE` replaces its criteria with one nonblank criterion per
line. Other template fields remain as supplied in JSON. Only one input can
read stdin (`-`). For example:

```sh
aeon recur create --body-file audit.json --tag website-audit \
  --description-file playbook.txt --criteria-file checklist.txt
```

A definition names a project and an immutable parent, a ticket/task template,
and a trigger. Time triggers accept `FREQ=DAILY`, `FREQ=WEEKLY;BYDAY=MO,FR`, or
`FREQ=MONTHLY;BYMONTHDAY=1,-1`, plus `time_of_day` (`HH:MM`), an IANA `timezone`,
and an optional `start_date` (defaults to today's local date). Omitted weekday
or month-day comes from that start date. Monthly rules also allow `INTERVAL=1`,
`2`, `3` or `6` (default `1`), stepping from the month of `start_date`; daily
and weekly rules reject `INTERVAL`. For example,
`FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1` runs on the last day every three months.
Other RRULE parts are rejected. Invalid
month dates and spring gaps are skipped; a repeated fall time uses its first
instant. Preview uses the same calendar and returns UTC timestamps without
writing anything, including while paused. Event triggers use
`{"kind":"event","event":"release.published"}`; future publication dates
cannot be previewed. Event triggers may set `event_start` to `now` (default),
`hour` or `morning`; morning means 06:00 on the next local calendar day in
`event_timezone` (an IANA zone, default UTC). The scheduler checks the database
clock inside each claim before running delayed publications, including across
DST changes. Deferred rows yield to other ready definitions without advancing
their event cursor.

An explicit release source list, for example
`{"kind":"event","event":"release.published","filter":{"project_ids":["<source project UUID>"]}}`,
subscribes the output project to each publication from up to 20 readable projects
in the same tenant. The saving caller and current definition owner need source
read permission. The scheduler rechecks source visibility and authority under
its tenant/tree fence; revoked or unreadable sources refuse without consuming
pending publications. Only bounded release identifiers, names and versions
reach generated work, never source ticket content.

Explicit subscriptions consume publications in event order. With overlap
suppression, an open ticket or unfinished routine run retains the next
publication without a skipped receipt or cursor advance. A manual release run
uses the same source-project/publication key; an overlap returns 409 so a later
retry can still create it. Release choices use the configured sources, return
at most 100 entries and disclose truncation. Omitted or empty source lists keep
legacy same-project latest-only catch-up and overlap receipts.

Automatic source subscriptions default off. This slice supplies
`WithReleaseSubscriptionGate`, a transaction-bound integration for S04's
person-owned output-project execution setting. Without that binding the
scheduler creates no subscription publication events or occurrences and keeps
its cursor. S04 is absent from this slice's baseline; the coordinator must wire
its live gate before automatic subscriptions are available. Saving a scoped
definition remains paused, and pending intents grant no launch authority.
Qualification and person enablement remain separate release controls.

For example, a weekly tool sweep under the code-health epic can be created with
this body (replace project/parent UUIDs with the intended existing nodes):

```json
{
  "project_id": "<project UUID>",
  "parent_id": "<code-health epic UUID>",
  "template": {
    "title": "Weekly tool sweep {{date}} (#{{occurrence}})",
    "description": "Run lint, gosec, govulncheck, semgrep and npm audit; compare the stored baseline and verify new findings.",
    "acceptance_criteria": ["Compare the baseline", "Verify and deduplicate new findings"],
    "estimate_hours": 0.34,
    "priority": "high",
    "type": "ticket"
  },
  "trigger": {"kind":"time","rrule":"FREQ=WEEKLY;BYDAY=MO","time_of_day":"09:00","timezone":"Europe/Vienna"},
  "queue_each": true,
  "overlap_policy": "skip",
  "catch_up_policy": "one"
}
```

The rolling deep read uses the same weekly trigger with its own template and
3-hour estimate. A per-release delta audit uses the event trigger and a template
such as `Delta audit {{release_name}} ({{release_version}})`, with a 1-hour
estimate. These are configuration examples; installing a release creates no
production audit schedules automatically. Template variables also work in
descriptions and criteria. `tags` contains existing tag-node UUIDs; occurrence
creation resolves their current names into ordinary ticket `fields.tags`.

Templates also accept optional `pill_en`, `pill_de`, `benefit_en`, `benefit_de`
and `hide_from_release_notes`. Recurring routines default to hidden, including
older definitions without the flag; explicit `false` makes generated tickets
visible in release notes. Repeat copies all five fields from the source, and
editing a definition preserves them. Release copy supports the same variables
as the title, with limits of 512 bytes per pill and 4096 bytes per benefit,
checked before and after rendering. Templates may omit or draft the copy;
generated tickets still need both 2–4-word pills and both nonblank benefits
before successful Done, including when hidden.

The server uses the database clock and tenant/tree claims, with durable unique
occurrence keys. Each pass consumes at most the latest missed time or release
per recurrence, avoiding a downtime burst. `overlap_policy=skip` suppresses work
while any earlier live occurrence ticket remains nonterminal; `create` allows
concurrent occurrences. Created and skipped attempts both increment the
occurrence number. Resume discards work accumulated while paused; manual run
now is allowed while paused and does not move the schedule. Queueing reuses
queue readiness, work orders and the inert queue holder, awaiting coordinator
routing. It requires a positive estimate and acceptance criteria.
Each tenant pass attempts at most 50 due rows. A failing row rolls back,
records a `recurrence.failed` audit event when its claim is available, and is
excluded for the rest of that pass so later rows can run. The pass reports
the accumulated failures. Its original cursor remains available for retry
on the next pass; failed attempts do not increment the occurrence number.

The configured product's immutable release history produces tenant/project-scoped
`release.published` events. Historical Journey publications remain stored; the
retired Flow / Journey (AEON-723) no longer publishes new releases. The same project and
version are deduplicated across sources. Newly configured schedules ignore
publications from before their creation. Occurrence tickets and their node,
queue and recurrence audit events use the keyless system actor **Recurring
work**. Occurrence receipts link each ticket back to its recurrence. Lists show
a gold loop badge on the type icon, in a fixed 22 px slot; its accessible tooltip
describes the schedule and occurrence number in English or German. The ticket
header shows a Recurring pill linking to the source recurrence only with
management permission. Node GET and list responses carry read-only `recurrence`
provenance from visible occurrence receipts, enriched once per bounded page;
editable fields do not create a marker or provenance line. Authoritative GETs
refresh source retirement and visibility even when the ticket revision is unchanged;
partial save responses preserve the current provenance. Source links and editing use
the receipt's project, including after a ticket moves. A linked recurrence is resolved
directly rather than substituted with the first page's first schedule. Retired
sources keep their markers; withheld provenance clears the marker and source editor.
Source editing becomes available when delayed management permissions arrive, without
reloading the recurrence details; revocation closes the editor and cancels pending edits.
The header pill keeps the same size through permission loading, grant and revocation;
on touch devices both the marker and link reserve a minimum 44 px height.
Status help and `aeon status help --json` share the same marker definition.
Management requires `recurrences.manage`; agents need an explicit custom-role grant plus
a key scope, even when bound to Owner/Admin. Saving a definition also requires
`nodes.write`; `queue_each` adds `run.create` and `work_orders.write`.

The recurrence tests cover calendar/DST edges, preview, replica and foreground
claim barriers, restart recovery, overlap/catch-up, publication deduplication,
queue/provenance, transaction rollback, tenant/project RLS and permission
changes during a write. They use an injected database clock and channels rather
than sleeps. Regression coverage includes monthly intervals across short months,
gaps and folds, tree-writer lock barriers, CLI template files/project tags, and
persistent schedule/publication/queue failures followed by valid due work.
The full Go suite validates the shared authorization, work-order,
queue, server and OpenAPI integration alongside this package.

AEON-573 fix-round evidence: the full Go suite passed at `2f00b466` on the
approved offload host with `GOMAXPROCS=6 go test -p 4 -timeout 30m ./...`.
Overlaying only the new test files on `a2bd6890` reproduces both tree-writer
deadlocks (`40P01`), rejects the tenant FK key-share fence (`NOWAIT`), rejects
monthly intervals and both CLI template writes, and starves the later valid
row for each persistent cursor, publication and queue failure. The fixed
regressions pass, including denial after the barrier-controlled member
demotion. Source transfer used Git archives over SSH, without pushing a ref.

DSAR integration note (AEON-490): the inventory is absent from this branch and
its `origin/main` baseline. Migration 1115 introduces `recurrences` and
`recurrence_occurrences`, both tenant-scoped with forced RLS and project
visibility. Classify `recurrences.template` and `created_by_principal_id` as
personal; other definition, schedule and cursor columns as metadata. Locate a
recurrence by `(tenant_id,id)`. Classify the user-supplied occurrence key as
personal and other occurrence receipt columns as metadata,
located by `(tenant_id,recurrence_id,occurrence_key)`; linked ticket contents and
actor identity remain personal data in the existing nodes/principals/events
inventory. The coordinator must add these entries when merging onto the DSAR
inventory. No new secret storage is introduced.


AEON-825 adds API/CLI JSON configuration for `node.done`, `knowledge.changed`,
`external.tag` and `external.deploy`. The existing recurrence editor still
creates time and release triggers; use JSON definitions for these new kinds.
Existing definitions display the correct event type and retain selectors/sender
configuration when editing templates or event delays. Manual Run now for the new
kinds creates a normal manual occurrence without offering a release picker.
New trigger configuration controls need Opus design.

A Done trigger derives a transition from actual node/status events into the
kind's configured Done category, including custom status names. Updates that
stay in Done and transitions to Cancelled do not fire. For example:

```json
{"kind":"event","event":"node.done","filter":{"project_ids":["<source project UUID>"],"has_release_copy":true,"exclude_hidden":true}}
```

The project list accepts at most 20 distinct UUIDs and defaults to the target
project. Release-copy filtering requires all four nonblank pills/benefits in
the source event snapshot. A knowledge trigger accepts `entry_id`,
`knowledge_type` and an exact `tag` from `fields.tags`; supplied conditions are
combined. Supported types are runbook, guideline, memory, external-system,
related-project and decision. Creation, content updates and archival changes
can fire; unavailable/deleted source nodes are withheld.

```json
{"kind":"event","event":"knowledge.changed","filter":{"knowledge_type":"guideline","tag":"website"}}
```

The definition owner needs read permission in every source project (and
`knowledge.read` for knowledge). The scheduler rechecks the owner's current
permissions and event-reference visibility under the tenant/tree fence before
writing a ticket. Invisible source events advance the cursor without copying
content; revoked authority reports a failed attempt and retains its cursor for
retry. New trigger kinds consume source events in order, within the existing
50-claim tenant pass. A source event has one durable occurrence key, including
an overlap skip. Replaying a cursor cannot create another occurrence. Pause
and trigger replacement discard accumulated events, as for release triggers.
Event delays apply to each source event's logged time. Created tickets append
a bounded JSON context containing the event ID and source node key/ID, knowledge
entry ID, release ID where available, or external delivery/source/ref. Source
bodies and arbitrary external payloads are never copied. A description that
would exceed the ordinary node limit produces a skipped receipt.

External senders use an existing agent key and an existing Ed25519 signing
authority; PAIMOS stores only the public key in `trigger.external`:

```json
{"kind":"event","event":"external.tag","external":{"principal_id":"<sender agent UUID>","public_key":"<base64 Ed25519 public key>"}}
```

Send normalized events to `POST /api/recurrences/{id}/events` with the existing
agent authentication and `X-Aeon-Signature`. The sender needs an explicit
`recurrences.manage` grant and `nodes.read` on the target project, with matching
key scopes. Sign the exact UTF-8 bytes of
`aeon-recurrence-event-v1\n<tenant UUID>\n<recurrence UUID>\n<request body>`
using Ed25519, and base64-encode the signature. A trusted product/site relay
maps its existing tag/deploy evidence to this envelope; native provider payloads
are not accepted directly:

```json
{"delivery_id":"delivery-123","event":"external.tag","occurred_at":"2026-10-10T12:00:00Z","source":"product/repository","ref":"v1.2.3"}
```

Use `external.deploy` only for successful deployments. Requests are bounded to
8192 bytes, delivery IDs to 128 bytes, and source/ref to 256 bytes each. The
signed time must be within five minutes of the database clock. Tenant and
recurrence IDs bind the signature, and the configured agent owns its delivery
receipt. Exact retries within that window return the original `202` event
receipt (`event_id`, `duplicate`); changing a delivery's payload returns `409`.
Paused definitions reject new deliveries. Retired definitions and foreign
UUIDs return `404`. Sender and owner permissions are checked again before
scheduling. Intake records an event only; neither intake nor the scheduler
launches an agent. Explicit `queue_each` retains the existing inert queue
handoff to coordinator routing. No schema migration or new secret is required.
