# Agent Intercom: Architecture and Operator Runbook

Agent Intercom is Paimos's durable, project-scoped path for agent messages and
owned local controls. The Paimos ledger is the source of truth; local delivery
workers and `paimos-agentd` apply a message to a receiver only after leasing
that durable work.

The base owned-session commands first appeared in 5.21.0. The complete surface
documented here—including scoped controls, generation worker leases, durable
reporting, control-outcome reads, and the M168 database guards—requires the
upcoming calendar release 26.09.01 or later. Do not use this guide as written
with 5.21.0 or 26.08.31. It uses only public command names and placeholder
identities. Keep actual target references, socket paths, credentials, and
message content out of documentation and logs.

## Fast path: owned Codex

Prerequisites:

- `paimos`, `paimos-agentd`, `jq`, and Codex are installed.
- `paimos auth login` has configured the Paimos instance.
- The coordinator and worker are registered project agents.
- An administrator has allowed `paimos:coordinator` to send to
  `codex:worker`.
- An authenticated Paimos administrator performs every message-target and
  delivery administration operation: `paimos message target set`,
  `paimos message target list`, `paimos message target requeue`,
  `paimos message deliveries`, and the per-delivery requeue endpoint. Project
  membership or agent attribution alone is insufficient; use separate
  least-privilege shells for senders and listeners.
- `AGENTD_SOCKET` names an absolute Unix socket in an owner-only directory.
  Do not commit or print it.
- For durable status and typed interrupt/stop, `REPORT_HOST` is a stable
  non-secret machine label, `REPORT_URL` is the exact HTTPS Paimos deployment
  URL, and `REPORT_API_KEY_FILE` is an absolute owner-only file containing its
  service-account API key. Keep the file path and contents out of logs.

Use one stable instance name everywhere. It selects both the remote Paimos
configuration and the isolated local agentd state.

Start the daemon in its own terminal:

```bash
INSTANCE=production
REPORT_HOST=worker-host
REPORT_URL=https://paimos.example.com
REPORT_API_KEY_FILE=/absolute/path/to/owner-only-api-key
paimos-agentd serve --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --report-host "$REPORT_HOST" --report-url "$REPORT_URL" \
  --report-api-key-file "$REPORT_API_KEY_FILE"
```

The three reporting flags are all-or-none. Agentd performs an authenticated
preflight and refuses to start if the exact URL or credential file is invalid.
Non-loopback reporting requires HTTPS, and all redirects are rejected. Use the
optional absolute `--paimos-path` only when the reporting CLI is not on
`PATH`. Omit the reporting trio for local-only status and control.

Start an owned Codex child from the repository it may work in. The prompt goes
through stdin, never process arguments:

```bash
INSTANCE=production
PROJECT=PAI
PROJECT_ID="$(paimos --json project show "$PROJECT" | jq -er '.id')"
ADDRESS=codex:worker
SESSION_ID="$({
  printf '%s' 'Work only on the assigned ticket.' |
    paimos-agentd start --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
      --adapter codex --workspace "$PWD" --project-id "$PROJECT_ID" \
      --identity "$ADDRESS"
} | jq -er '.id')"

paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET"
```

Wait until that session reports `state=running`, `steerable=true`, and a
non-empty `sessions[].harness_session_id` before steering it. In local agentd
status, `harness_session_id` is the vendor Codex thread or Claude session ID;
it is not the public durable harness generation.

For the durable bus, register two receiver-owned targets:

1. `agentd_codex` / `agentd_session` / `steer` as `primary`.
2. `codex` / `codex_thread` / `simple` as `simple_fallback`.

The primary reference contains the private agentd socket and local session ID.
The fallback reference is the vendor Codex thread ID in local agentd
`sessions[].harness_session_id`. Never substitute the separately reported
`sessions[].reporter.public_session_id`, which names the public durable
generation. Build both references in the pipeline and pass them through stdin
so no capability file remains on disk; never print or commit either value.
Target registration is an administrator-only setup step:

```bash
jq -n --arg socket "$AGENTD_SOCKET" --arg session_id "$SESSION_ID" \
  '{socket: $socket, session_id: $session_id}' |
  paimos message target set --project "$PROJECT" --address "$ADDRESS" \
    --adapter agentd_codex --kind agentd_session --maximum-level steer \
    --role primary --target-ref-file -

paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET" |
  jq -er --arg id "$SESSION_ID" \
    '.sessions[] | select(.id == $id) | .harness_session_id' |
  paimos message target set --project "$PROJECT" --address "$ADDRESS" \
    --adapter codex --kind codex_thread --maximum-level simple \
    --role simple_fallback --target-ref-file -
```

Run both receiver workers. The steer-only worker handles live interruption; the
simple worker handles ordinary messages and truthful fallback. Each advances
the inbox cursor only after its matching handoff succeeds.

```bash
paimos listen --as "$ADDRESS" --project PAI --follow --deliver agentd_codex
paimos listen --as "$ADDRESS" --project PAI --follow --deliver codex
```

In the sender shell, establish attribution and send the durable message:

```bash
eval "$(paimos session start --project PAI --agent coordinator)"
paimos tell codex:worker --project PAI --level steer \
  --message 'Re-check the current acceptance criteria.'
```

`tell` returns the canonical `message_id`. The delivery row has a distinct
`delivery_id`; the listener passes that ID to agentd as the steer correlation
and records the final effective level and fallback reason.

### Local status and controls

These commands operate on the exact child owned by this daemon. They return a
structured local receipt. Use a stable, non-secret correlation ID so the
effect can be matched with operator records; use `tell` plus the listeners
above when the text itself must be durable in Paimos.

Every control repeats the exact instance, numeric project ID, and identity
bound at start. Agentd rejects a scope mismatch before the child sees it.
Retrying an identical operation and text with the same correlation ID returns
the original receipt without a second vendor effect. Reusing that ID for a
different operation or text fails closed.

```bash
paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET"

printf '%s' 'Pause and re-check the current diff.' |
  paimos-agentd steer --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
    --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
    --correlation-id operator-steer-001

paimos-agentd interrupt --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --correlation-id operator-interrupt-001

paimos-agentd stop --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --correlation-id operator-stop-001
```

`interrupt` stops the current turn; `stop` closes the owned harness and reaps
the process group. Neither command invents a vendor session from a PID.

## Architecture

```text
attributed sender
      |
      | paimos tell
      v
Paimos API -- allowlist, hold, size, hop, and scope checks
      |
      +-- canonical message ledger (message_id, thread_id, cursor)
      |
      +-- delivery row (delivery_id, immutable target snapshots, state)
                         |
                         | attributed FIFO lease
                         v
                  receiver-side listener
                         |
                         | private local capability
                         v
                    paimos-agentd
                         |
                         | exact owned Process / Query
                         v
                   Codex or Claude
```

The layers have separate responsibilities:

| Layer | Owns | Does not own |
|---|---|---|
| Message ledger | Canonical content, sender/receiver attribution, thread and hop, security decision, durable cursor | Vendor process or target secret |
| Delivery coordinator | One delivery ID, immutable primary/fallback target versions, FIFO lease, attempts, typed result | A second message body or model response |
| Delivery adapter | One documented vendor handoff primitive and its truthful effective level | Authorization, target selection, or queue-faked steer |
| Harness control plane | Durable managed/unmanaged identity, advertised capabilities, heartbeat, typed interrupt/stop requests, and a digest of the private generation lease | A worker lease, vendor reference, or local Process/Query handle |
| `paimos-agentd` | Exact local child Process/Query bound to instance, numeric project, and identity; private Unix transport; local status, replay-safe control receipts, and an owner-only per-generation worker lease | Paimos database, shared API-key contents, or authority to adopt an old PID |

`paimos harness` is the durable integration surface for workers that report
M161 harness-session state. It is separate from `paimos session start`, which
only establishes request attribution, and from the local agentd session ID.

### IDs are not interchangeable

| ID | Meaning |
|---|---|
| `message_id` | One canonical, project-scoped message |
| `delivery_id` | The retry/lease record for that message; also the managed bus steer correlation |
| `thread_id` | Paimos conversation and hop chain, never a vendor thread |
| agentd session ID | One child owned by one running daemon instance |
| agentd status `sessions[].harness_session_id` | Vendor Codex thread or Claude session; receiver capability, not a public generation |
| agentd status `sessions[].reporter.public_session_id` | Public durable control-plane generation; the Paimos harness-session ID |
| harness API `harness_session_id` | Public durable generation on a control-plane response |
| control ID | Durable interrupt/stop request and exact agentd correlation UUID |
| worker lease | Private authorization proof for one public harness-session generation |

Do not substitute one ID for another. In particular, a harness-session row or
PID is audit/status evidence, not proof that a new daemon owns the old process.

## Trust boundaries

- The sender comes from authenticated Paimos agent attribution. The message
  body cannot choose it. A receiver allowlist is prospective: allowing a
  sender does not release an older held message.
- Listen and acknowledge are project-scoped and require the attributed agent
  to match the receiver. Cursor updates are monotonic.
- Messages are untrusted data. They cannot grant permission, enable tools, or
  approve an action. Typed action requests are held for a human; secret-like
  bodies, oversized bodies, and excessive hops fail closed.
- Targets belong to the receiver, are versioned, and are encrypted at rest.
  Ordinary target and delivery listings are redacted. A sender chooses
  `simple` or `steer`, never a target, socket, vendor session, or policy cap.
- Paimos server rows are isolated by deployment instance and project. Agentd
  hashes its instance value into a separate private state/socket directory, so
  two configured instances do not share ownership history. Each child also
  binds its numeric project ID and attributed identity; every control and
  receipt must match the complete scope tuple.
- A public harness-session UUID plus caller-supplied agent attribution is not
  worker authority. Every worker mutation must match the project, public
  harness-session UUID, attributed agent, and a distinct per-generation worker
  lease. The lease is neither the vendor session reference nor the shared API
  key. Missing, duplicate, wrong-generation, or cross-project proof fails
  closed with one uniform non-enumerating `403` and no mutation.
- Agentd keeps each worker lease in its instance-scoped, owner-only local state
  store. It never enters argv, a URL, ordinary status, logs, or the lifecycle
  journal. Paimos stores only a domain-separated digest. Secret-bearing worker
  requests reject redirects, so a lease is never forwarded to another origin.
- Agentd stores bounded, content-free lifecycle evidence. It does not persist
  prompts, responses, credentials, target references, or arbitrary vendor
  errors. Claude durable text does not enable Bash.
- A successful handoff means the documented receiver primitive accepted the
  input. It does not prove the model understood, obeyed, or completed it.

## Supported and unsupported behavior

| Receiver | Simple | Steer | Status | Interrupt / stop | Boundary |
|---|---|---|---|---|---|
| Durable inbox only | Read and acknowledge | No vendor control | No process status | No | Message remains a framed, untrusted inbox item |
| Owned Codex (`agentd_codex`) | Separate `codex` fallback target and worker required | Yes, exact live app-server turn | Local always; durable harness heartbeat when reporting is enabled | Local exact owned process; durable typed interrupt/stop when reporting is enabled | Reporter advertises status/interrupt/stop, never inbox/steer |
| Owned Claude (`agentd_claude`) | Separate valid simple target and worker required | Yes, exact live Agent SDK Query | Local always; durable harness heartbeat when reporting is enabled | Local exact owned Query/process; durable typed interrupt/stop when reporting is enabled | Reporter advertises status/interrupt/stop, never inbox/steer; pinned SDK required |
| Unmanaged Codex | Yes, documented queue primitive | Yes only for a bound target using documented external steer | Only if its integration reports status | No owned interrupt/stop | Cannot claim process ownership |
| Unmanaged Claude (`claude_resume` / `claude_channel`) | Yes | No; requested steer records effective `simple` with `unsupported` | Only if its integration reports status | No | Resume/channel handoff is never called steer |
| Grok Bot routine / gated Grok Build path | Wake or new-turn handoff only | No; effective behavior is simple | No owned process status | No | A webhook or CLI resume is never queue-faked as steer |

Managed agentd targets are deliberately steer-only. Without the separate
simple fallback target and matching listener, an ordinary message cannot be
truthfully handed off and remains recoverable instead of being mislabeled.

### Durable reporting and worker authorization

When authenticated M161 reporting is enabled, the shared Paimos API key and
the generation worker lease have different jobs. The API key authenticates the
service account and is read by the reporting CLI from a separate protected
credential file. It does not authorize a harness worker mutation by itself.
Agentd mints one worker lease for each local owned session, keeps it in its
private local credential store, and sends it only through protected stdin and
the authenticated worker request. A new owned child gets a new lease.

The reporter registers and heartbeats only `status`, `interrupt`, and `stop`.
It yields typed durable interrupt/stop requests, applies them to the exactly
scoped owned child, validates the receipt, and completes the durable control.
It deliberately advertises neither `inbox` nor `steer`: durable steer remains
the message target/listener path, so reporting cannot create a second route or
turn a simple handoff into steer.

The server binds the lease digest to the exact project, public harness-session
UUID, and attributed agent. Heartbeat, yield, delivery drain/completion,
control completion, and stop all require that complete scope. Registration
with the same stable external reference is an idempotent replay only with the
same generation lease; a different lease cannot take over the live row.

Inbox-capable registration is target-first and recoverable. The server first
creates or reuses the encrypted `managed_harness` target, then commits the
first active harness-session row with both its worker-lease digest and target
foreign key. A crash before that insert can leave only the reusable encrypted
target, which grants no worker authority; an exact retry reuses it. Database
guards reject both insertion and later mutation of an inbox-capable session
without its target, and the generation lease digest is immutable.

Reporter acknowledgements are evidence, not hints. Agentd validates the
returned public session, project, agent, harness, phase, control kind, state,
reason, and delivery/control identifiers as applicable before advancing its
local checkpoint. A redirect, malformed body, mismatched successful response,
or ambiguous transport result remains a retryable reporter failure and cannot
authorize a local or remote state transition.

### Worker activity truth

Harness phase, control leasing, and worker activity are deliberately separate.
`working` says the owned generation may report; it does not mean a model is
busy. `yielded` says the reporter checked for durable controls; it does not mean
the model is idle. The reporter therefore publishes its ordinary heartbeat
before yielding controls, so a failed yield cannot suppress liveness and the
yield phase cannot overwrite the independent activity conclusion.

The durable activity projection has four closed states:

| State | Required evidence |
|---|---|
| `busy` | A monotonically newer, documented adapter `turn_started`, `tool_started`, or `control_applied` event from the current owned generation |
| `idle` | A monotonically newer, documented adapter `turn_completed` event from the current owned generation |
| `unknown` | No activity report, process-only `session_started`, malformed or stale evidence, unmanaged evidence, or a heartbeat older than 90 seconds |
| `dead` | Reporter-confirmed process exit, process failure, ownership loss, or explicit owned stop |

Silence is never interpreted as busy or idle. Ordinary daemon heartbeat ticks
do not refresh the adapter activity timestamp or sequence. A heartbeat appends
only when phase or activity truth changes, preventing periodic no-op ticks from
growing the log; yield, control completion, stop, and activity-timeout
transitions append the resulting content-free projection in the same
transaction. Those rows cannot be updated or deleted directly, while deleting
their parent session cascades them. Session Home schema version 2
returns the state, safe reason, evidence age, and terminal reason. If no live
generation remains it may show the latest reporter-confirmed dead generation,
but an unmanaged, unreported, stale, malformed, or ambiguous worker remains
`unknown`.

An owned integration reports only the adapter's monotonic, content-free event
sequence and a documented kind. `session_started` proves process ownership but
does not invent busy or idle. The generation lease stays on protected stdin:

```bash
paimos harness heartbeat --project "$PROJECT" \
  --session '<public-harness-session-id>' --agent worker \
  --worker-lease-file "$WORKER_LEASE_FILE" --phase working \
  --activity-sequence 42 --activity-kind turn_started
```

## Diagnostics

All commands below return non-secret or redacted state. The message target and
delivery listings are still administrator-only; redaction does not make them
available to a project member, sender, or receiver. Run those two lines only in
an authenticated administrator shell:

```bash
paimos doctor
paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET"
paimos message target list --project PAI --address codex:worker
paimos message deliveries --project PAI
paimos harness list --project PAI
paimos harness status --project PAI --session '<public-harness-session-id>'
paimos harness control get --project PAI \
  --session '<public-harness-session-id>' --control-id '<control-id>'
```

Interpret delivery state before changing anything:

| State | Meaning | First response |
|---|---|---|
| `pending` | Awaiting the matching receiver worker | Start or repair that listener |
| `leased` | A worker owns a time-bounded attempt | Let the worker finish; after a crash, lease expiry makes it recoverable |
| `retry` | Same delivery is waiting for its next attempt | Fix the typed transport error; do not resend blindly |
| `blocked` | No valid route or a closed policy boundary | Check target roles, caps, fallback reason, and instance/project |
| `handed_off` | Receiver primitive accepted the input | Do not requeue; this is handoff, not model completion |
| `dead` | Automatic attempts are exhausted | Restore the snapshotted target or make an explicit operator recovery decision |

Useful fields are `state`, `requested_level`, `effective_level`,
`fallback_reason`, `attempt_count`, `last_error_code`, and timestamps. Listings
never need the target reference or message body to diagnose routing.

Local agentd status also exposes bounded reporter health and content-free
checkpoint state. A growing reporter failure count means local ownership may
still be live while durable status/control publication is unavailable. Repair
the protected API-key file, exact report destination, DNS/TLS, or network
access; do not replace the generation lease or manually mark the row healthy.

With reporting enabled, `paimos harness list --project "$PROJECT"` discovers
the public generation and `paimos harness status` reads its durable heartbeat.
An authorized coordinator may request a durable interrupt or stop; the
reporter later claims and completes it against the exact owned child:

```bash
PUBLIC_SESSION_ID='<public-harness-session-id>'
CONTROL_ID="$(
  paimos harness interrupt --project "$PROJECT" \
    --session "$PUBLIC_SESSION_ID" | jq -er '.id'
)"
paimos harness control get --project "$PROJECT" \
  --session "$PUBLIC_SESSION_ID" --control-id "$CONTROL_ID"

# To terminate the child instead, request stop and inspect its returned ID.
CONTROL_ID="$(
  paimos harness stop --project "$PROJECT" \
    --session "$PUBLIC_SESSION_ID" | jq -er '.id'
)"
paimos harness control get --project "$PROJECT" \
  --session "$PUBLIC_SESSION_ID" --control-id "$CONTROL_ID"
```

`harness interrupt` and `harness stop` return the initial pending request; that
acceptance is not proof of the local effect. Read the same request with
`harness control get` until it is terminal, and inspect local agentd status.
The read is bound to the exact project, public session, and control UUID. It
returns only `id`, `project_id`, `harness_session_id`, `correlation_id`,
`sequence`, `kind`, `state`, optional terminal `outcome` and `reason`, and
request/claim/completion timestamps. The correlation ID equals the control
UUID. Use the direct agentd commands for an immediate local receipt.

Agentd retains at most 256 control correlation outcomes per live session,
including successful receipts and remembered failures, and never evicts one
while that session remains controllable. Reuse the original correlation ID for
an exact retry; agentd returns the original success or failure without invoking
the vendor again. A conflicting reuse or exhausted bound fails closed;
gracefully stop the daemon-owned child and start a fresh session instead of
changing local state.

## Recovery

### Listener stopped or crashed

Restart the same `paimos listen --follow --deliver ...` worker. A message is
acknowledged only after successful output or adapter handoff. An expired lease
returns to the same durable delivery; do not send a duplicate message merely
because a listener restarted.

### Daemon restarted

Every session recovered from a prior daemon is marked `ownership_lost`, with
PID and steer/interrupt/stop authority removed. This is deliberate. Start a
fresh child, register a new primary target version, and restart the matching
listener. Never copy a PID or edit agentd state to adopt the old process.

If the recovered session had a durable harness generation, agentd reopens its
private per-generation worker lease only to finish conservative recovery. It
first completes any journaled claimed-control outcome with its exact recorded
result, then marks the old remote generation stopped, validates the exact
acknowledgement, and deletes the local lease. It never re-registers the lost
child as live. A fresh owned child receives a distinct lease and public
generation.

Upgrades fail closed for legacy harness rows that have no worker-lease digest:
active rows become stopped and pending or claimed controls become rejected
with `ownership_lost`. They cannot be revived by presenting the public UUID,
agent name, shared API key, vendor reference, or a newly invented lease.

The Paimos message/delivery row survives independently in SQLite. If it has a
snapshotted simple fallback, an unavailable managed lease can reroute to that
fallback. A steer-capable harness generation is eligible for managed reroute
only while its heartbeat is no more than 90 seconds old and its authenticated
activity projection is `busy` or `idle`; `unknown` and `dead` never qualify,
and its phase may independently be `yielded`.

### Target was missing

An authenticated administrator must register the receiver's primary and
fallback targets, then explicitly attach them to never-attempted
`blocked/target_missing` rows:

```bash
paimos message target requeue --project PAI --address codex:worker
```

Target registration alone never mutates historical deliveries.

### Target is stale or a delivery is dead

All inspection, target registration, target requeue, and per-delivery requeue
in this recovery path require an authenticated administrator. Registering a
replacement creates a new target version for new messages; it does not rewrite
a target already snapshotted onto an attempted delivery. Restore the original
receiver target before requeueing that delivery. The administrator-only
endpoint `POST /api/projects/{id}/message-deliveries/{deliveryID}/requeue`
reuses the same delivery ID and snapshot; it does not retarget. If the original
target cannot be restored, inspect whether any handoff may have occurred and
send a new message only as an explicit operator decision.

### Stop and recreate a durable harness generation

After the integration has cleaned up its owned process, close the old public
generation with `paimos harness mark-stopped`. The worker mutation requires
the exact generation lease from an owner-only file; a shared API key and agent
name are insufficient. Registering the same stable external reference with a
new lease then creates a new public generation while retaining stopped-row
history. Unmanaged sessions can never gain interrupt/stop through this process.

```bash
paimos harness mark-stopped --project "$PROJECT" \
  --session '<public-harness-session-id>' --agent worker \
  --worker-lease-file "$WORKER_LEASE_FILE" --reason process_exited
```

If the server committed that close before the reporter could journal
`remote_closed`, a retry preserves the first immutable terminal reason even if
the local process is now classified differently.

Agentd terminal cleanup is ordered and restart-safe:

1. `remote_closed`: the server accepted stop and agentd validated the exact
   stopped-generation acknowledgement, after any journaled claimed-control
   completion was settled.
2. `lease_deleted`: the private local worker lease was durably removed.
3. `prunable`: only now may bounded terminal history be removed to admit a new
   session.

A crash between steps resumes from the durable checkpoint. Agentd never
deletes the lease before remote closure is proven, and it never prunes a row
that might still need its lease to finish recovery.

### Rollback

Pause delivery workers before a server/database rollback and follow
[Deploy/Rollback](DEPLOY.md) plus [Backup/Restore](BACKUP_RESTORE.md). The
SQLite backup boundary contains the durable ledger and delivery state. Agentd
ownership is not restorable evidence: after rollback or daemon replacement,
start fresh owned children and register fresh target versions. Verify with
`paimos doctor`, redacted delivery state, and agentd status before resuming
listeners.

## Executable evidence

The ordinary backend suite covers the documentation contract:

```bash
cd backend
go test -count=1 ./cmd/paimos ./cmd/paimos-agentd ./contracts
go test -count=1 ./db -run '^TestMigration168RetiresUnboundGenerationsAndEnforcesLeaseDigest$'
go test -count=1 ./db -run '^TestMigration169BackfillsTerminalTruthAndRejectsInconsistentActivity$'
go test -count=1 ./handlers -run '^(TestHarnessWorkerMutationsUseUniformNonEnumeratingAuthorization|TestGetHarnessControlReturnsScopedNonSecretOutcome)$'
go test -race -count=1 ./agentd ./agentmessage ./managedharness
```

High-signal proofs include:

- `TestBusAgentdManagedSteerUsesLeaseAndCanonicalCompletion`
- `TestBusAgentdClaudeSteerCarriesDurableMessageAndDeliveryIDs`
- `TestSupervisorRestartReconcilesPersistedChildrenToOwnershipLost`
- `TestSupervisorStateIsSeparatedByPPMInstance`
- `TestSupervisorRejectsCrossScopeControlBeforeOwnedProcess`
- `TestSupervisorReplaysCompletedControlReceiptWithoutDuplicateEffect`
- `TestCLIReporterRegistersExactScopeAndAppliesTypedControl`
- `TestCLIReporterRestartRetriesJournaledCompletionWithoutSecondEffect`
- `TestCLIReporterRemoteCompletionSuccessThenClearFailureRecoversWithoutSecondEffect`
- `TestCLIReporterRemoteCloseWaitsForDurableLeaseReleaseBeforePrunable`
- `TestDiskReporterLeaseStorePersistsAndRejectsUnsafeCustody`
- `TestHarnessWorkerLeaseRejectsSpoofMissingDuplicateAndCrossSessionProof`
- `TestHarnessLeaseRequestsNeverFollowRedirects`
- `TestHarnessWorkerMutationsUseUniformNonEnumeratingAuthorization`
- `TestHarnessActivityRequiresTypedCurrentEvidence`
- `TestHarnessSessionEventsAreTransactionalImmutableAndPhaseIndependent`
- `TestConcurrentActivityTimeoutAppendsOneTransition`
- `TestHarnessControlGetUsesExactReadOnlyScopedRoute`
- `TestGetHarnessControlReturnsScopedNonSecretOutcome`
- `TestMigration168RetiresUnboundGenerationsAndEnforcesLeaseDigest`
- `TestRegisterRecoversAfterTargetCommitBeforeSessionInsert`
- `TestStopRacesControlRequestWithoutStrandingControl`
- `TestClaudeProcessBindsSteerAndInterruptToOneLiveQuery`
- `TestUnavailableAgentdIgnoresStaleWorkingGeneration`
- `TestUnmanagedSessionCannotRequestOwnedControl`
- `TestAgentMessageDeliveryWorkSchemaIncludesBothOwnedAgentdAdapters`

The CLI tests also parse the quickstart contract so renaming a documented
verb or required flag fails CI instead of silently drifting.
