# Agent work estimates

Setting a ticket/task estimate also fills missing `route_role`, `area` and `complexity`. The server uses title and string labels/tags, then the nearest ancestor's classification or title hints, then `build` / `backend`. Explicit values win. Every server suggestion carries `<field>_source: "suggested"`, the acting principal's `<field>_by`, UTC `<field>_at`, and `<field>_confirmed: false`. A person can confirm a suggestion or an unconfirmed agent classification by resubmitting the same value without source/by/at, or with `aeon issue update KEY --role build --area backend --complexity S`. Agent values remain unconfirmed; client-supplied stamps cannot confer confirmation. Existing ticket rows are not backfilled.

Complexity defaults to **S** for estimates up to 2 hours, **M** above 2 and up to 8 hours, and **L** above 8 hours. `build-hard` promotes one bucket (S → M, M → L, L → L). This is a deterministic suggestion, not a measured model-performance claim. Unconfirmed server-derived complexity follows estimate or role changes; explicit complexity retains its provenance. `aeon issue update KEY --estimate 2 --role build --area backend --complexity S` supplies all three, and issue JSON returns their stamps and confirmation flags.

Harness sessions return `model_profile_id` for a unique tenant registry match by harness, model and effort. Recognized trailing effort suffixes (`low`, `medium`, `high`, `xhigh`, `max`, `ultra`) move into `reasoning_effort`; `model_raw` retains the cleaned original string for audit. Unknown models, absent effort, conflicting explicit effort, and ambiguous profile versions stay unresolved. There is no inferred model alias or cross-tenant match. Model/effort heartbeats refresh the identity. An effort-only heartbeat preserves the stored original model string when a legacy session has no `model_raw`. The additive migration 1063 backfills only uniquely matched sessions, under tenant and project RLS, without changing registration replay digests. `SELECT aeon_backfill_session_model_profiles()` can repeat inside an authorized tenant transaction; it returns the updated-row count and leaves resolved and unknown rows unchanged.

Estimates are expected **agent hours until ready for review**, separate from a live ETA.
Set them with `aeon issue create ... --estimate-hours 2`, `aeon issue update AEON-317 --estimate-hours 1.5`, or `aeon issue estimate AEON-317 --hours 1.5 --source agent`. `--estimate 90m` remains an alias. Decimal hours and minutes are accepted; values must be greater than zero and at most 200 hours. The optional source asserts the authenticated principal kind; the server stamps the principal and time. Creating a ticket or task without an estimate prints a non-blocking warning. API callers can PATCH `/api/nodes/{id}` with `{"estimate_hours":2}` to change just the hours, or null to clear them; other fields survive. Do not combine this property with a replacement `fields` document.

Settings → Workspace → **Agent activity** applies to every project: **Off** collects and shows no activity, **Tool activity** uses sanitized tool observations without summary tokens, and **Agent summary** (default) prefers a public phrase for ten minutes, then uses tool activity. Heartbeat and attached-session replies include `agent_activity_mode`; agents should spend summary tokens only when it is `agent_summary`. `harness heartbeat --doing "Running Go tests"` reports a phrase of at most 60 characters. The heartbeat helper reads optional `doing` from its existing status file, anchoring freshness to the file's modification time, so an unchanged file never makes an old phrase fresh. Managed workers can query and report through `aeon_activity`. Hook observations live only in the current generation's existing private state directory. Approved conversation tails classify new tool envelopes; status-only attachment never opens a transcript. Automatic payloads contain only fixed phrases or safe source-file basenames, never raw commands, arguments, environment values or file contents. The agents table shows current activity below the name; details keep up to twenty changes and coordinators summarize their active workers. The existing `activity` busy/idle field remains unchanged; the additive phrase is `current_activity: {text, source, at}`.

Agent summaries, tool basenames and legacy heartbeat notes share one credential filter on the server and browser. It rejects credential words, known key prefixes, opaque tokens, credential URLs, environment assignments, invisible Unicode format characters and combining/enclosing marks. Status-file notes are screened before truncation, incoming notes before storage, and stored activity and histories before display; an unsafe summary falls back to valid tool activity. Legacy notes retain their 120-character limit and control stripping. Activity history follows the session's tenant and project visibility for both reads and writes.

`aeon harness run-heartbeat --status-file .agent-status.json` reads `pct`, `remaining_min` and `note` on every beat. Bound workers send the percent and a ready ETA anchored to the file's modification time plus the remaining minutes, including zero. An unchanged file's ETA can become overdue; after 30 minutes without an update, the CLI warns `stale_progress`. Minutes must be finite numbers from 0 to 524160 (364 days); ETAs outside the server's allowed window are omitted. Workers with `--worktree` default to that worktree's `.agent-status.json`; coordinators read a file only with an explicit `--status-file` and retain their separate live-ETA reporting. Missing, malformed or refused status files never stop the loop. If the server rejects status-file estimates, the CLI retries the heartbeat without those fields. Status reads retain the credential, symlink and hard-link fence.

Heartbeat responses carry non-blocking `warnings`: a working worker gets `missing_progress` after three accepted beats without a fresh percent, a working session with a bound `ticket_node_id` gets `missing_eta` for its role's absent ETA, and a visible bound ticket/task without valid hours gets `ticket_without_estimate`. Unbound workers and coordinators have no missing-ETA warning or UI hint. Warning-query failures are logged and return an empty array without rolling back the heartbeat. Both heartbeat commands print each code to stderr at most once per ten minutes, with receipts retained across reporter restarts. Run-heartbeat uses its private state directory; one-shot heartbeats use private 0700 directories under `~/.aeon`, including when the lease comes from stdin.

Coordinators report **ticket live**, including review, merge and deployment, with
`aeon harness heartbeat ... --eta-live +20m`. Their ETA does not mean the
coordinator process has finished. Coordinator beats reject `--progress` and
`--eta-ready` with an explanation: percent done is derived from their direct
worker children on the same bound ticket. Active workers and cleanly finished
workers count equally; unreported progress counts as zero. Failed, removed and
other-ticket children are excluded; no eligible children leaves progress absent.
The mean is rounded to the nearest integer and includes children outside the
current list page. This is a read projection; it never marks a coordinator Done.

Every coordinator can register AI, media and terminal children using the same
worker lease and parent/ticket/ETA/progress protocol. AI agents keep their
existing harness (`codex`, `claude`, `pi`, `cursor`, `grok`) and model flags.
Media uses `--harness media --generator higgsfield/kling3_0`; terminal uses
`--harness terminal --command ffmpeg`. Public labels are at most 120 ASCII
characters: letters, digits, `.`, `_`, `:`, `/`, `+`, `-` (terminal also permits
spaces). Start with a letter or digit; omit surrounding whitespace. These are
labels, never secrets or executable command arguments. Media/terminal require
`--role worker`, `--parent-session`, a ticket and a work shape; they neither
inherit `AEON_MODEL`/`AEON_EFFORT` nor look up the model registry.

For example, while a media job is running, start its reporter:

```sh
aeon harness run-heartbeat --owner-pid "$job_pid" --state-dir "$job_state_dir" \
  --project AEON --agent worker --harness media --generator higgsfield/kling3_0 \
  --parent-session "$coordinator_session" --ticket AEON-501 --work-shape ship \
  --status-file "$job_status_file" --host "$(hostname -s)"
```

The terminal equivalent uses `--harness terminal --command ffmpeg`; AI workers
use their usual harness and model. `harness register` accepts these same family
and label flags with its usual private registration files. Follow-up one-shot
heartbeats use the returned session id and lease with `--eta-ready +10m
--progress 40`, or keep `pct`, `remaining_min` and `note` current in the reporter's
status file. Each concurrent job needs its own private state directory.

Lead decision evidence (AEON-737) uses the existing project event log. An active
coordinator generation records bounded, typed metadata with
`aeon harness decision --project AEON --session UUID --body-file decision.json
--worker-lease-file PATH`. Only its owning agent and exact generation lease can
write; current project permission is checked inside the fenced transaction,
including on replay. Identical `request_id` metadata replays its original event;
different metadata or a different generation returns a conflict.
Migration `1263` enforces request uniqueness per tenant and project even when
a referenced ticket moves out of the coordinator's visibility. Reusing that
identity for otherwise valid evidence returns a redacted 409, retaining the
hidden snapshot and rolling back the failed append's event-counter allocation.
Replay and history still obey event visibility and current authorization.

`aeon project decisions AEON --limit 50 [--after EVENT-ID] [--session UUID]`
reads a page of redacted reports, with `--json` preserving all evidence and the
next-page cursor. Project members can read policy scope/revision, attempts,
reason codes, gate observation times and same-project result links. Review
links freeze the existing exact base/head commit and provider-family binding.
`previous_event_id` links a report to an earlier decision for the same ticket;
attempts may stay the same or advance by one, capped at 32. Existing run IDs
identify assignments. A `partial` outcome requires `partial_result`; unavailable
forecasts, model escalation, login selection and connection/process failures
retain separate reason codes. Prompts, arbitrary
URLs, account identities and quota values are rejected. Admission reports
require dial, harness, account-room and host-load readings; unreadable readings,
missing timestamps and readings older than 120 seconds or in the future produce
a visible wait. Freshness stays frozen when replayed. These are coordinator
reports, not launch receipts, verified review verdicts or execution grants;
recording never starts work, opens a review gate, merges or deploys. The future
lead executor must supply this evidence alongside its existing admission path.
Automatic production launch remains gated by AEON-603 and OPS qualification.

The Sessions table retains its existing design, adds a separate **Host** column,
and calls the intended result column **Name**. Host identity is the exact
registered `--host`; `run-heartbeat` defaults to the short OS hostname (the part
before its first dot), without querying macOS ComputerName. People can click the
badge or its floating pencil to set **Your name for this computer**. Save applies
to all rows of that host for that person; **Use '<registered host>'** resets it.
Labels are stored server-side under tenant/person/host with person-only RLS.
`GET /api/me/host-labels` reads only your overrides; `PUT` with `{host, label}`
saves one and a null label resets it. Both require `harness.read`; a project-only
role with that permission suffices. Agents cannot read or write overrides.

Harness status and heartbeat return the response-only header `Aeon-Contract: harness-session/2.8`; no request header is required. Existing reporters keep working without a Pharos or Janus release. The warnings field is optional in the shared session schema and is returned as an array on heartbeats; existing response fields and request requirements are unchanged. 1.8 included `finished`, a required response boolean that is always present, false included, in every session, live and event payload (derived in SQL from a reported 100% and a recorded clean exit); readers that ignore it are unaffected, and no screen derives Done from `progress_pct` or `stop_reason`. It also adds the optional `row_version` (AEON-449): the session row's own revision, raised by the database inside every statement that changes the row, so the larger of two copies is the newer. Reporters may ignore it. 1.9 adds optional nullable `model_raw` and `model_profile_id` for auditable model identity; request requirements stay unchanged. 2.0 declares the expanded harness enum, including Gemini CLI and OpenCode (AEON-452), and adds the optional `generator` and `command` response labels and media/terminal families; existing fields remain intact. 2.1 adds optional `current_activity`, `current_activity_history` and `agent_activity_mode` for current activity reporting; request requirements stay unchanged. 2.5 adds optional `service_tier`, `service_tier_revision` and per-model `service_tier_reports`; 2.6 adds optional nullable `service_tier_request` for pending agent requests on list rows. 2.8 adds optional commit diff counts. 2.9 adds the optional response-only `agent_recovery` diagnosis (AEON-731): the reporting-loss cause and the recovery action a paired daemon can carry out; request requirements stay unchanged. 2.7 adds optional response-only `owner_principal_id` for the reviewed pause and wind-down ownership projection and optional `desk_answers` references for durable Decision Desk handovers. Existing reporters and request requirements stay unchanged. The pin checker records expansion of an existing enum as a major schema change; existing harness values still register and heartbeat unchanged.

Pause levels (AEON-524 part A2) add `--level stop_now|pause_quickly|pause|wrap_up`
and an optional `--note` to pause one or all sessions. Omitted levels use the
person's workspace default, initially Pause; read or change it with
`aeon harness pause-default [--level pause_quickly]`. Pause quickly interrupts
where the harness advertises support, commits WIP as it is and requests a short
handover within three minutes. Pause chooses the next safe point within ten
minutes. Wrap up finishes only when the estimate is below ten minutes and fits
the deadline; otherwise it hands over. The API exposes `supported_pause_levels`
and `pause_can_interrupt`; workers without a cooperative inbox support only
Stop now. Pause all skips those workers unless Stop now is selected.

`aeon harness leaving-at --at 2026-10-02T17:00:00+02:00 [--note "Continue tomorrow"]`
sets one durable deadline for all of your running work in authorized projects.
Fresh finish estimates select Wrap up first, including near the deadline when
finishing fits with one minute to spare. Otherwise Pause starts at T minus ten
minutes; fresh handover and command timing choose Pause quickly when a full
handover cannot fit. Without that timing, escalation starts at T minus two
minutes. A current uninterruptible command keeps cooperative Pause, without an
interrupt signal. Stop now is requested at each level’s deadline. Pause and
Wrap up end at the earlier of the leave time and ten minutes after their actual
start; Pause quickly has a three-minute cap, with a two-minute escalation lead.
Workers without an inbox keep working until T. `aeon harness leaving-at` reads the deadline and
per-agent plans; `--off` withdraws pending requests and leaves paused handovers
resumable. A claimed stop cannot be recalled and is reported as in flight.
Setting the same deadline and selection again is idempotent; changing either
replaces pending plans. A deadline covers at most 200 owned running sessions
atomically, and rejects a larger selection before changing any plans.

Workers can include optional `step`, `next_point`, `next_point_in_min`,
`finish_in_min`, `finish_outcome`, `interrupt`, `command` and `command_left_min`
in a heartbeat. These form the optional `pause_progress` response snapshot,
with a database-clock `reported_at`; the current operation's `command` does not
change a terminal session's registered command label. Sending any planning
field replaces the snapshot (omitted fields are unknown, null clears); omitting
all of them preserves its original timestamp. Minutes count down from that
instant and expire after two ETA intervals. Once a planning snapshot exists,
an absent or stale finish prediction never falls back to an older ready ETA.
Legacy reporters without a snapshot retain their fresh ready-ETA behavior.
Text is limited to public labels without credentials, paths or command arguments.

The leaving API also accepts `{hosts: "all" | [host identities], agents: [session
UUIDs]}` alongside `deadline_at`. `agents`, when provided, selects the exact
owned running generations independently of `hosts`; ended and unauthorized
sessions are ignored. Omitted `agents` selects running work on the named hosts,
or all owned work when `hosts` is omitted. An empty agent array selects no work.
GET returns the effective selection and the stored host scope. Host scope is
persisted for the approved wind-down UI and later launcher admission integration;
this backend does not yet enforce refusal of new starts on those hosts.

Stop now uses the existing control queue. Agentd executes it only for its exact
live generation and owned process identity, with the existing monotonic signal
fence. A database-clock wake hint schedules the next heartbeat at each transition.
`harness run` advertises `owned_stop_v1` and may stop only the child it launched; a PID-only
`run-heartbeat` observer receives no signal authority. Requests, scheduling,
escalation, cancellation, defaults and confirmed outcomes are audited. A stop
request is not a process exit receipt: offline or older launchers can leave an
unconfirmed request, which remains visible in the deadline report. The server
never claims such a process has stopped. Existing part-A records without a
level retain their original cancel-on-expiry behavior. The response contract
adds optional planning fields at `harness-session/2.4`; contract 2.5 adds the
optional nullable `owner_principal_id` for ownership-aware previews. Request
headers and requirements stay unchanged.

Pause/resume (AEON-524 part A) adds optional `pause` and `continuation` fields
in contract 2.2. `pause.state` distinguishes requested, planned, paused,
resume_requested, resumed and cancelled; the established phase values remain
unchanged. `state=paused` selects resumable closed generations, including those
older than 24 hours in `view=current`; existing stopped filters remain compatible.

```sh
aeon harness pause --project AEON --session SESSION_UUID --reason "Reboot"
aeon harness pause --all --except KEEP_RUNNING_SESSION_UUID
aeon harness resume --project AEON --all
```

A person needs `harness.control` and owns the registration (project owners/admins
may control other registrations). An agent coordinator uses `--project`,
`--coordinator-session` and its `--worker-lease-file` to control direct children.
Global `--all` spans the person's visible authorized projects; `--project` narrows
it. Batches return up to 200 items and `more`; repeat while `more` is true.
Requests and the default ten-minute handover deadline persist in Postgres.
The pause control uses the existing `stop` kind with `request_payload.pause=true`; managed
yield holds it aside so it cannot accidentally trigger an immediate signal.
Heartbeat delivers the durable request. `run-heartbeat --print-controls` emits
an `aeon.harness-pause.v1` JSON record; otherwise it prints guidance on stderr.
Agentd forwards a heartbeat pause through its existing generation-fenced inbox
when the adapter supports input, with the ordinary durable input receipt.
Managed sessions must advertise `inbox`; otherwise an individual pause returns
409 without creating a request. Project and person-wide pause-all leave these
sessions running, continue pausing capable sessions, and report the skipped
session's `id`, `project_id`, optional `display_label` and reason
`inbox_delivery_unavailable` in `skipped`. Reports are capped at 200 per call;
`skipped_more` indicates omitted reports. Skips do not consume the 200-pause
limit or set `more`, so repeating a batch can reach later capable sessions.
Cursor and Grok daemon runs currently lack that input path. Unmanaged CLI
heartbeat sessions advertise cooperative pause only with `--print-controls`;
one-shot `harness run` jobs advertise Stop now only.

An overdue requested or planned pause becomes `cancelled` on the database clock.
Heartbeat, session/control reads and the periodic sweep complete its cooperative
control with reason `pause_deadline_expired`. Level-aware requests also queue
Stop now; historical requests without a level retain cancel-only expiry. Before
the deadline, a
`heartbeat_lost` close preserves the pending or claimed pause control so the
same worker can heartbeat, plan and stop with its handover after revival.
An unarchived paused or resume-requested generation continues to occupy its
ticket, suppressing the status autopilot's stale-progress rule until it is
resumed or archived.

The worker must plan a safe stopping point, finish or roll back the current
step, commit WIP on its own branch, then submit a handover and exit cleanly:

```sh
aeon harness pause-plan --project AEON --session SESSION_UUID --agent AGENT_NAME --worker-lease-file LEASE_PATH --control-id CONTROL_UUID --handover-point "After the current commit"
aeon harness mark-stopped --project AEON --session SESSION_UUID --agent AGENT_NAME --worker-lease-file LEASE_PATH --reason paused --handover-file HANDOVER_JSON_PATH
```

The handover JSON contains `state`, a nonempty `next_steps` array,
`open_questions` (an empty array is valid), and `worktree_state` (`committed`,
`clean` or `rolled_back`). `committed` requires `commit_sha`. Session storage,
the bound-ticket comment, control completion and stopped generation are one
transaction; a retry cannot duplicate the handover. Notes are public project
content: never include credentials or private registration proofs.

`resume` persists a launch recipe and full continuation brief. For one session,
`--registration-file` may supply a fresh private reference and lease and create
the successor atomically; ordinary `harness register --succeeds SESSION_UUID`
and `run-heartbeat --succeeds` also consume that recipe. The successor keeps
the principal, harness/model, generator/command labels, branch/worktree and
ticket, and receives the full handover in `continuation.brief` rather than truncating it to the short
metadata label. Old run and process ownership are never reused. Resume paused
coordinators before their paused children; the new coordinator adopts those
children for continuation. Generation registration does not itself launch a
vendor executable. Agentd automatic launch after login and a hard-stop deadline executor
are separate runtime integration: any hard stop must use the existing exact
owned-process controls, and these HTTP endpoints never signal a process.

Reporter pins identify response schemas by their `METHOD /path status` labels.
`RequiredBump` treats a new required response property as a minor addition:
the server supplies it, and existing harness clients ignore extra response fields.
Components also referenced by any request body, including through nested schema
or reusable request-body references, retain request rules throughout their
expanded subtree. Pins store this provenance separately from the response hash.
New required properties on these shared components still require a major bump.
Changed existing properties, newly requiring an existing optional property, and adding a required
property to a previously closed response schema also remain major changes.

Agent drafts show `est.` until a person or a working agent bound to the ticket confirms or changes them. Resubmitting the hours through the estimate command confirms them; provenance is recorded again. The ticket's Estimate control also edits or clears the value. Parents show the sum of their visible, non-archived, non-cancelled leaves at any depth, including done leaves. A typed parent estimate stays alongside as “planned” and never enters the leaf sum. Progress weights estimated leaves by hours and shows coverage; with no estimates it counts leaves. Regrouping the same leaves does not change project progress. Ready/live ETA is the latest report among open leaves, labelled partial when some have no report. Weighted or rounded 100% does not suppress that ETA or staleness while leaves remain open. Project-scoped readers receive authorized Autopilot Undo facts so these projections refresh even when parent status stays unchanged. Headline and state-facet counts (including Hide) count leaves only; parents group work. Historical spend stays on former leaves and each session counts once per scope, including cancelled/archived work; exact-money visibility still requires access to both the row and source projects. The invoker-rights `aeon_work_scope` traversal deduplicates requested roots and has no depth cutoff; `aeon_work_aggregates` shares leaf facts/session reads and returns explicit coverage. Migration 1230 preserves both old `aeon_node_eta` and `aeon_node_eta_progress` signatures for previous binaries. The Estimate sort keeps empty values last in either direction. Imported points remain visible as points, not converted to hours.

Ticket details show the same progress, ETA and leaf coverage below the controls. A disconnected event feed labels the ETA as the last reported estimate. People with write access open **More actions → Work actions** to split or cancel work; closing the sheet returns keyboard focus to More actions.

For a backfill, an agent drafts a JSON plan such as `[{"key":"AEON-317","hours":2},{"key":"AEON-318","hours":0.5}]`, then runs:

```sh
aeon issue estimate --missing --project AEON --from-file plan.json --dry-run
aeon issue estimate --missing --project AEON --from-file plan.json --apply
```

Both modes validate every plan entry and project membership before any write. Apply uses the agent identity, skips work already estimated and checks each node's revision. A concurrent change stops the plan; earlier successful writes remain applied and a rerun skips them. There are no server-side model calls.
