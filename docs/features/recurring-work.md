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
