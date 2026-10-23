# One-off runs

Agents → New → **Run once** is available to a person with `run.create` and
`work_orders.write`, without enabling the developer manual-start setting. The
expert **Start agent manually** entry remains available under that setting.

A run always belongs to a work leaf. Enter a short task first, then choose an
existing ticket or a project. The latter creates a visible `Run once: <task>`
ticket with `hide_from_release_notes=true`. Creating that ticket additionally
requires `nodes.write`. The dialog does not grant permissions or change an
account's permissions.

The separate instruction is optional and limited to 2,000 characters. A supplied
instruction is retained exactly. In the dialog an empty instruction uses the
entered task; with no task, it uses the selected ticket's current body, or its
title if the body is empty. The direct work-order run API
uses that work order's body/title as its fallback. Newly created orders copy the
ticket text, falling back to the ticket title. The selected context and its revision, person,
account, profile, instruction and file permission are captured on submission.
Failures retain the choices. Changing context resets the instruction and file
permission while keeping the entered task. Changing person clears all choices;
a late response cannot start or populate another person's dialog.

**May change files** is off by default. The read-only ceiling currently qualifies
Claude only: an empty native/MCP tool inventory, deny hooks, no inherited
settings or plugins, and a scratch directory outside the approved repository.
It analyzes the supplied task context without tools, repository reads, external
actions or file mutations. This is ordinary managed work, with neither review
privileges nor pairing-verification identity. Other harnesses show the server's
qualification reason and refuse read-only startup. Selecting **May change
files** uses the ordinary managed tools and workspace checks.

`POST /nodes/{nodeId}/run-once` composes normal node creation, work-order
preparation and the existing run writer in one transaction. The server derives
the run UUID from the tenant and request UUID; the existing unique key makes identical retries return the same
run even after a lost response or completion. The run's personal-data-classified
`trace.run_once` binds the exact request digest, effective instruction and ceiling.
Changed input with the same UUID returns 409. An active leaf run prevents a
different request from starting concurrently. No migration is required.

Current person/computer, leaf, account, host, residency, budget and start checks
remain authoritative. Final claim repeats the initiating person's current
`run.create` and `work_orders.write` checks under the access-change fence. The
daemon must advertise `run_once_v1`; old daemons cannot pick up or claim these
runs. One-off runs require a fresh Run once request to restart; automatic vendor
continuation and the generic restart path cannot widen the saved ceiling.

Terminal telemetry records one `run_once.settled` event and one ticket comment
per run, linking its session in Agents (including History), or its run record
when no session was registered. Successful runs move the leaf to **Done**
in the same transaction, provided the initiating person still has `nodes.write`
on its current project and the normal Done entry checks pass. This never supplies
a person override, grants a permission, or changes a parent's status directly.
Failed, cancelled and ownership-lost runs retain the ticket's status and record
the reported error code (or terminal status). Cancelling a queued run records the
reason `cancelled_by_person`. Replaying either terminal telemetry or queued
cancellation creates no duplicate event, comment or status transition.

The run pins the leaf's identity, state and revision at launch. **Any ticket edit
after launch preserves its status**, including a person changing the state away
and back. A changed binding, a new work child, an already terminal ticket, missing
permission, failed Done check or unconfirmed process exit also preserves its
status; the result comment records why. Historical runs without a launch binding
can record a result but cannot auto-close. Deleted tickets are never restored.
The ticket remains the durable record after a successful run.

Host, harness, account, model and thinking use the existing computed defaults,
shown in one summary. **Change** opens these settings below it; a quota, sign-in,
wait or read-only qualification warning opens them automatically. Project
revisions refresh each time the dialog opens. Both launch modes show the
platform's submit keycaps. A queued run's wait override is labelled **Start now,
skip the wait**.

Malformed persisted one-off bindings are skipped and logged on run lists;
pagination continues past them. Direct reads and claims still refuse them.

The dialog reuses the existing account cascade and shared controls. No design was
attached to AEON-1512: **needs Opus design** and Tick's visual review after CI.
