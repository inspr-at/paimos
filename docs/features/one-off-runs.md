# One-off runs

Agents → New → **Run once** is available to a person with `run.create` and
`work_orders.write`, without enabling the developer manual-start setting. The
expert **Start agent manually** entry remains available under that setting.

A run always belongs to a work leaf. Choose an existing ticket, or choose a
project and enter a short task. The latter creates a visible `Run once: <task>`
ticket with `hide_from_release_notes=true`. Creating that ticket additionally
requires `nodes.write`. The dialog does not grant permissions or change an
account's permissions.

The separate instruction is optional and limited to 2,000 characters. A supplied
instruction is retained exactly. An empty instruction uses the work-order body,
or its title if the body is empty. Newly created orders copy the ticket text,
falling back to the ticket title. The selected context and its revision, person,
account, profile, instruction and file permission are captured on submission.
Failures retain the choices. Changing context or person resets the task choices;
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
preparation and the existing run writer in one transaction. The request UUID is
the run UUID; the existing unique key makes identical retries return the same
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

The dialog reuses the existing account cascade and shared controls. No design was
attached to AEON-1512: **needs Opus design** and Tick's visual review after CI.
