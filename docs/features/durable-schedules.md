<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Durable schedules (AEON-1628)

Recurring agent work belongs to a recurrence with a saved routine assignment.
The server creates a durable occurrence and run intent; the existing routine
executor routes it and agentd claims it. The recurrence, occurrence receipts,
consent and launch intents survive session and daemon restarts. No session cron
is needed. The existing project qualification, person enablement, definition
consent, account, host and admission gates still apply. Saving a definition
starts it paused and grants no execution authority.

Time triggers now also accept `FREQ=MINUTELY;INTERVAL=1..1440` and
`FREQ=HOURLY;INTERVAL=1..168`; omitted intervals mean one. These use elapsed
time anchored to `start_date` and `time_of_day` in the named timezone. The
anchor must exist; a fall fold uses its first instant. Later ticks retain
their elapsed spacing across DST. Existing daily, weekly and monthly schedules
retain their calendar semantics. Missed ticks coalesce to the latest due tick,
with the existing unique occurrence receipt and overlap policy.

The In-flight refresh cadence can be expressed as this JSON definition for
`aeon recur create --body-file in-flight.json`, after substituting authorized
project, parent, owner and work-kind UUIDs:

```json
{
  "project_id": "<project UUID>",
  "parent_id": "<operations parent UUID>",
  "template": {
    "title": "Refresh In-flight {{date}} (#{{occurrence}})",
    "description": "Read current delivery and worker receipts; update the In-flight knowledge entry with observed progress and unresolved waits.",
    "acceptance_criteria": ["Refresh from current receipts and identify unresolved waits"],
    "estimate_hours": 0.1,
    "priority": "medium",
    "type": "work"
  },
  "trigger": {
    "kind": "time",
    "rrule": "FREQ=MINUTELY;INTERVAL=15",
    "time_of_day": "00:00",
    "timezone": "Europe/Vienna"
  },
  "queue_each": true,
  "overlap_policy": "skip",
  "catch_up_policy": "one",
  "definition": {
    "scope": {"kind": "project", "project_id": "<project UUID>"},
    "owner_principal_id": "<person UUID>",
    "assignment": {
      "goal": "Refresh the In-flight knowledge entry from current receipts without changing delivery approvals or running a session cron.",
      "sources": [],
      "role": "build",
      "work_kind_id": "<work-kind UUID>",
      "allowed_actions": ["knowledge.write"],
      "runtime_requirements": {"needs_native_host": true, "runtime_class": "native_coding"},
      "budget": {"mode": "off"}
    }
  }
}
```

AEON-1672 already preserves person authority: authenticated full-access keys
derive recurrence management only in writable projects and remain bounded by
their creator and acting person's current grants. Scoped keys need an explicit
recurrence role grant and key scope. A keyless scheduler owner still requires
explicit current recurrence authority. This change adds no recurrence grants.

New questions accept `remind_after_seconds` from zero to 2,592,000 (30 days).
Zero disables the reminder; omission snapshots the workspace default when a
fresh question is created. A matching asker joins the existing schedule without
resetting its deadline; explicitly different reminder settings keep open
questions separate. Request replay preserves the original schedule. Existing
questions from older writers are not backfilled.

For example, a durable “remind Markus after 2 h” nag is the ordinary question
input plus `"remind_after_seconds": 7200`. The person is resolved by the current
Desk routing policy: the held-message recipient for source-linked questions,
otherwise the project's current human owner. No arbitrary recipient or session
is accepted. The two former 2 h/6 h nag patterns can be expressed through CLI
data as follows, using their respective questions:

```sh
aeon ask --project OPS 'Proceed with the pending setup?' \
  --option '["Proceed","","Proceed with the setup."]' --remind-after 2h
aeon ask --project OPS 'Finish the pending follow-up?' \
  --option '["Finish","","Finish the follow-up."]' --remind-after 6h
```

The question dispatcher scans reminders together with its existing answer
outbox, retaining one idle queue transaction per reconciliation. At most 32
due reminders are checked per tenant pass. Final writes recheck the current
question, project, active canonical recipient and read/decision/inbox authority
under tenant/tree/question locks. Reminder acceptance and the interval receipt
commit atomically. A failed acceptance returns an error and rolls both back;
its next check backs off for 30 seconds without consuming the interval, so a
page of persistent failures cannot starve later healthy reminders.
Replicas and retries cannot accept a second notice for that interval. After
downtime, one reminder is accepted and the next interval starts from acceptance;
there is no catch-up burst.

Quiet hours defer acceptance without incrementing the reminder count. The
recipient's existing phone quiet-hour preferences take precedence, including
when phone push is disabled. Otherwise the workspace timezone and quiet hours
apply. Overnight windows and DST gaps/folds are resolved in local time. Quiet
ends are exclusive; equal endpoints disable the window.

Acceptance writes one System inbox notice containing only the Desk question
link and a `question.reminded` event. The open canonical question is ordered
ahead of other unheld questions in the Desk without changing its answer
revision or held-work state. The `reminder` projection exposes `after_seconds`, accepted `count`,
optional `last_at` and `next_at`; count records durable acceptance, not successful
external transport. Answering clears the next reminder in the answer transaction.

`GET /api/settings/question-reminders` reads workspace defaults for a person
with `settings.read` or `settings.manage`. `PUT` needs a signed-in person with
`settings.manage` and the current `expected_revision`. It accepts
`default_after_seconds`, `time_zone`, `quiet_start` and `quiet_end` (local minutes
of day). Defaults are 7,200 seconds, UTC, 22:00–08:00. Changes affect future
questions' default intervals and current fallback quiet hours. Agent keys
cannot change these defaults. The per-question Desk control remains a later UI
slice; this implementation uses the API/CLI and existing Desk and inbox surfaces.

These are configuration examples, not installed production schedules. Migration
1423 adds tenant-scoped settings and project-scoped reminder state with forced
RLS; both are classified in the DSAR inventory. No session cron, new secret,
execution enablement, push or deployment is part of this slice.


Regression coverage exercises competing replica claims at the tenant fence,
acceptance rollback and fair retry, quiet-hour and DST boundaries, restart and
downtime coalescing, current recipient permissions, answer cancellation, Desk
ordering, and CLI/MCP parity. A scheduled 15-minute occurrence feeds the existing
routine dispatch and coordinator pickup test; agentd's existing routine tests
exercise its workspace and launch handling. Detached controls removing elapsed
timing, Desk promotion and the final interval check fail their corresponding
behaviour assertions. All original top-level test registrations are retained;
no shared tier manifest or browser spec is changed.
