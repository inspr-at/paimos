<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Chat live stream and final outbox

The opt-in chat module adds `GET /api/chat-threads/{id}/live`. Two viewers of
the same person's conversation receive the same normalized agent updates.
Every frame and heartbeat checks current participant and project access.
At most four live authorization checks run concurrently (fewer with a small
database pool). Their read snapshots end before any socket write; slow viewers
hold no database connection or tenant write fence.
Interim text and tool activity stay in bounded RAM; they never enter the
database, audit events, telemetry or logs.

Reconnect with `Last-Event-ID` or `after`. Replay resumes strictly after that
caller/thread-scoped cursor. Each conversation holds at most 64 frames and
256 KiB; the server admits at most 256 buffers and 256 viewers, with at most
64 buffers and 64 viewers per tenant and four viewers per conversation.
Capacity pressure evicts the oldest buffer without viewers, within the tenant
when its own cap is reached. Active buffers are retained. Body-free final,
receipt and read-marker hints do not allocate a missing buffer. Idle buffers
expire after two minutes on subsequent relay activity. Restart or a replay gap
returns HTTP 409 before streaming, or sends `resync` and closes an existing
stream. Reload final history before reconnecting
without a cursor. Streams rotate after five minutes; writes have a three-second
deadline. Replay from a superseded, stopped or stale session sends
`resync` with reason `binding_changed` and a cursor beyond the obsolete frame,
then closes without its content. Reload final history before a fresh live
subscription. A viewer losing access closes silently. Replay is a transient
convenience, not a durable transcript.

The existing external worker lease and exact current binding authorize
`POST /api/chat-deliveries/live`, which accepts the S1 normalized event DTO.
Only update frames with a nonzero source sequence are admitted. Duplicate or
older source sequences do not rebroadcast within the retained session/epoch
buffer. Capability snapshots are excluded. This is a server ingress seam;
daemon transport wiring and the composer belong to the subsequent slices.

`POST /api/chat-threads/{id}/outbox` stores a person's final input, while
`POST /api/chat-deliveries/final` stores an explicit final agent response.
Client IDs replay stable message IDs; changed payloads conflict. Only these
final bodies enter chat-isolated inbox rows. Final bodies are limited to 64 KiB
of valid UTF-8 without NUL; wire bodies are bounded before database acquisition.
The ordinary inbox, wake and managed-drain transports cannot consume them.

`GET /api/chat-threads/{id}/outbox` exposes bounded final messages and receipts.
The lease-authenticated `POST /api/chat-deliveries/outbox` reads only inputs
pinned to the current session; a successor never replays another session's
input. Retrieval does not change a receipt. Explicit recipient evidence at
`POST /api/chat-deliveries/outbox/receipt` advances `sent` to `delivered`, then
`read`; repeated or older evidence cannot downgrade it. A person's exact
visible-message read-marker union supplies person read evidence. Final-message,
receipt and read-marker notifications contain no message body; clients fetch
the durable outbox/history to reconcile them.

These routes preserve the disabled-by-default chat gate. They do not launch,
resume, steer, stop or automatically resend an agent's work. No schema
migration or release version change is required for this server slice.

## Daemon integration prerequisites (AEON-1074)

The daemon relay is not connected yet. The current S1 stream and server ingress
have incompatible registration boundaries: `Supervisor.SubscribeChat` exposes
supervisor-owned managed runs, while chat selection and every worker ingress
require a live external **unmanaged** registration. `ExternalRegistrationTx`
enforces this distinction, and `TestBindingRejectsForeignWrongRoleManagedAndStaleRegistration`
checks that a managed registration cannot be selected. Reclassifying an owned
run as external or removing this check would change the ownership policy.

The current `POST /api/chat-deliveries/binding/resolve` verifies a supplied
conversation ID, session ID and binding epoch; it does not discover a binding
from an owned session. The daemon currently has no approved source for that
conversation ID and epoch. Integration needs an explicit, authorized binding
handoff and a decision about which registration boundary supplies the stream.
Both must preserve exact-session ownership, the private worker lease, current
permissions and binding-epoch checks.

S1 supplies chunks, tool activity, capability snapshots and running/idle/action
state only. It supplies no explicit final-response or recipient-receipt event.
An idle state does not establish a final response, and fetching or queueing an
input does not establish delivery or read. Integration also needs explicit
final-response and recipient evidence sources. Interim chunks must never be
assembled into a persisted final body or copied into the daemon journal.

## Upgrade order and reverse proxies

The required rollout supports either upgrade order. A new daemon talking to a
release 128 server must detect missing relay routes, disable only the relay and
emit one content-free diagnostic. Runs, verification and existing final-message
delivery must continue. An older daemon talking to the new server supplies no
live frames; the live view must show **live view unavailable** without treating
the absent stream as an execution or final-delivery failure. These are pending
daemon/UI integration acceptance requirements, not a tested compatibility claim
for the server ingress alone. Until those prerequisites and both mixed-version
tests are complete, this slice does not establish an end-to-end live-chat rollout.

The live endpoint returns `text/event-stream` and flushes each frame. Keepalives
arrive every 15 seconds; set proxy idle timeouts above 15 seconds. Each stream
ends after five minutes, so the client reconnects using its last cursor through
`Last-Event-ID` or `after`, following the resync rules above when replay expires.
Disable response buffering for event streams; the server sends
`X-Accel-Buffering: no`. Caddy automatically flushes event-stream responses and
needs no `flush_interval` setting for this route. Preserve authentication and
cursor headers, and do not cache the stream or log its response bodies.

## Acceptance and validation

`TestChatLiveViewersReplayFinalOnlyAndReceipts` exercises two actual HTTP SSE
viewers, resume through `Last-Event-ID`, duplicate source suppression, an
expired replay cursor, database non-persistence of interim content, wrong
person/tenant/lease/epoch refusals, current key-scope revocation, final-message
idempotency, bounded outbox pages, monotonic receipts and active-viewer access
revocation. `TestChatLiveFiftyThreadsRemainBounded` concurrently floods 50 RAM
relay threads, checks frame/byte/subscription bounds and proves idle cleanup
with an injected clock.
`TestChatLiveFiftyHTTPViewersReleaseDatabaseBeforeWrite` holds 50 HTTP handlers
at transport barriers while a tenant mutation and acquisition of all 16 pool
connections complete. `TestChatLiveTenantCapacityAndIdleEviction` checks tenant
quotas, idle eviction and body-free hints, including an injected-clock proof
that a hint does not recreate an expired buffer. The handover, stopped-session
and expired-heartbeat cases in `TestChatLiveObsoleteBindingReplayRequestsResync`
require explicit resync without obsolete content. These three regressions
fail against the unchanged pre-fix production code at `7d8d1952`.

The affected `internal/chat`, `internal/agentd`, `internal/authz` and
`internal/reportercontract` packages passed on the approved remote test lane
after the fix round.
The locked `ci-static --merge-main` check passed all 42 checks without skips;
ownership, test-tier and web-shard checks also passed. No migration was added,
and existing pinned response schemas remain unchanged.

Remote snapshot preparation must generate the ignored OpenAPI bundle with
`node api/generate.mjs --write` before running contract tests. Maintenance
database identifiers must be SQL-safe or quoted. The coordinator's runner
currently omits bundle preparation and uses an unquoted hyphenated database
name. Validation used an in-memory adaptation for those two prerequisites,
preserving its canonical script identity and all host, hold, load, capacity,
cache and cleanup guards. The shared runner was not edited.
