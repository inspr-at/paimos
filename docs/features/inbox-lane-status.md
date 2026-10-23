# Lane status through the inbox

Agents can publish structured progress to a recipient using `aeon tell`. Each
report belongs to an exact active sender session and a goal or work item. The
report is an inbox message with `kind: "status"`, rather than free chat.

A report contains what was completed since the previous report, a blocker and
who can resolve it, the next step, and an ETA. Empty blocker and blocker owner
mean unblocked. `--eta unknown` records a null ETA. A report never changes the
work item's state, a parent's state, or the target's own ETA fields.

For example, JoeDesk can send every update during a day to LEAD's principal
inbox without a chat relay:

```sh
aeon tell "$LEAD_PRINCIPAL_ID" --inbox \
  --kind status --sender-session "$JOEDESK_SESSION_ID" \
  --target AEON-1629 --done 'Contract and handler completed' \
  --blocker 'API decision pending' --blocker-owner LEAD \
  --next 'Run the integration checks' --eta 2026-10-23T18:00:00+02:00 \
  --idempotency-key joedesk-2026-10-23-midday
```

On the next report, a new idempotency key and the progress since midday are
used. Once the decision arrives, both blocker flags are omitted. Morning,
midday, unblock and completion reports arrive through the recipient's normal
`aeon listen --inbox` and are retained as durable inbox messages. Acknowledged
messages leave the pending inbox; the latest lane status stays available.
Exact retries reuse the same key and message ID;
a changed report with that key conflicts and does not refresh its age.

Project addressing also supports the status flags:

```sh
aeon tell codex:lead --project AEON --kind status \
  --sender-session "$JOEDESK_SESSION_ID" --target AEON-1629 \
  --done 'Checks passed' --next 'Coordinator handover' --eta unknown
```

Project mode can use the current exact Aeon session binding if
`--sender-session` is omitted. Direct `--inbox` mode requires that flag and
does not discover a session. A target UUID avoids the additional node-key
lookup. Status reports cannot also be ordinary message text, replies, action
requests, attached notes, expiring messages or reply obligations.

The API accepts the same optional `kind` and `lane_status` fields on
`POST /api/inbox/messages` and `POST /api/projects/{projectId}/messages`.
`lane_status` carries `target_node_id`, `done`, `blocker`, `blocker_owner`,
`next` and `eta`. The server renders the inbox body from those fields. Ordinary
messages retain their existing response shape, with both new fields absent.

`GET /api/inbox/lanes` returns the latest accepted status for each session and
target pair. Each row includes the line, structured fields, message ID,
`reported_at`, `age_seconds` and `silent_seconds`; `as_of` is the database clock
used for the response. Only active, unarchived sessions with visible targets
are returned. Ticket-bound sessions that have never reported are included:
their status, reported time and age are null, and silence starts at session
creation. A session can have separate lanes for its ticket and a goal.

```sh
aeon lanes --target AEON-1629 --json
aeon lanes silent --minutes 30 --json
aeon lanes --silent-minutes 30 --session "$JOEDESK_SESSION_ID" --json
```

The silence filter is strictly greater than the threshold. Chat, heartbeats,
acknowledgements, delivery failures and idempotent retries never reset it.
Delivery receipts continue to describe whether the recipient received a
message; acceptance of a status does not claim that delivery succeeded.

The API accepts `target`, `session`, `silent_minutes`, `limit` (1–100, default
50), and `after`. A nonempty `next_after` means another page is available;
clients pass it as `after` with the same filters. An empty cursor ends the
list. This query supplies check-in data without introducing a check-in cron.

Status is intentionally published to readers with `inbox.read`, `harness.read`
and `nodes.read` in its scope. Ordinary private inbox chat is excluded. Sending
requires `inbox.send`, ownership of the active sender session and `nodes.read`
on the current target; project targets must belong to that session's project.
The final transaction rechecks authority under the access-change fence.
Tenant and target/session visibility policies apply to the current projection.

This server/CLI slice does not add Goals or Delivery UI rows. The clock-driven
integration subtest exercises a full reporting day entirely through inbox
messages and verifies status/ETA reads without relaying. It extends the
existing registered session test; CLI coverage likewise extends the registered
inbox CLI test, so this slice does not change tier manifests.
