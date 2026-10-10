<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Chat live stream and final outbox

The server setting `AEON_CHAT_ENABLED` defaults to `false`; set it to `true` to enable chat routes (only `true` or `false` are accepted; unset or empty means off).

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

The private worker lease of a chat registration and the exact current binding
authorize `POST /api/chat-deliveries/live`, which accepts the S1 normalized
event DTO. Only update frames with a nonzero source sequence are admitted.
Duplicate or older source sequences do not rebroadcast within the retained
session/epoch buffer. Capability snapshots are excluded, and so are final
answers, which travel only through the final-message route. The agentd relay
below feeds this ingress; the composer belongs to a subsequent slice.

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

## Daemon relay (AEON-1074)

agentd relays the chat of its own runs. A run whose harness supplies the S1
stream, may hold a chat binding and names the input each turn consumes
(Claude, Codex, Cursor or Grok) registers with the `chat` capability. The
server's chat-only registration check admits such a managed run, or an unmanaged registration with
`inbox`, under the same conditions: the caller is the session's agent
principal with its private worker lease, an owner person is set, and the
session is live with a heartbeat younger than two minutes. Other harness
routes still admit only unmanaged registrations.

At run start the relay asks `POST /api/chat-deliveries/binding/current` for its
own session's conversation ID and binding epoch, authorised like the other
relay routes. It never sees another session's binding and receives no message
content. While a run is unbound it asks again at most every 15 seconds, plus
up to 3 seconds of jitter. A 404 or 409 from a relay route triggers a new
lookup; an item refused twice is dropped.

The relay keeps its own bounded RAM queue: up to 64 live frames, eight final
answers and 32 receipts. When the queue is full it drops the oldest live frame
and reports the count in `dropped_events`. Transient failures back off from
250 ms, doubling up to 30 seconds, with jitter. A live frame is given up after
three attempts; a final answer retries until the run ends, then for at most
five more seconds. Nothing the relay carries is logged, journaled or kept after
the run.

**Final answers** come only from the harness's own turn-completion record. For
Claude this is the SDK `result` of a successful turn. For Codex it is the last
completed agent message of the owned turn, released by that turn's successful
`turn/completed`; commentary-phase messages are skipped. Deltas are never
assembled into a final. Cursor and Grok have no such marker, so their
runs deliver live frames only. Each final carries a client message ID derived from the session and
stream sequence, so a retried final persists once.

**Person inputs** are read from the worker outbox and, for runs that accept
inbox input, written to the harness only while it is idle, through the same journaled inbox control as other
harness input, so a crash never re-injects one. **Delivered** is reported when
the harness starts a turn after that write, **read** when that turn completes.
Fetching or queueing an input is no evidence, nor is a turn another input
started while this write waited. If no turn starts within two
minutes, the input stays `sent`. Person read evidence remains the read markers.

**Pi** is not relayed. Its turn markers do not say which queued input started
a turn, so agentd neither advertises `chat` for a Pi run nor writes person
chat input to it; its chat has no live view, as with a daemon that predates
the relay. Ordinary inbox and steering input to Pi works as before. Live chat
for Pi follows in AEON-1095. The same holds for any harness added later until its turn start
names the input it consumes.

## Upgrade order and reverse proxies

Either upgrade order is safe; both mixed versions are tested.

- **New agentd, release 128 server.** The older server rejects the `chat`
  capability. agentd registers again without it, so runs, verification and
  existing final delivery continue, and it writes one content-free log line.
  A server that accepts the capability but lacks the relay routes, or has chat
  switched off, disables only the relay. The relay is offered again after ten
  minutes, so a server upgraded in the meantime gets live chat without a
  daemon restart. Tested by
  `TestNewAgentdAgainstServerWithoutRelayKeepsRunsAndLogsOnce`.
- **Older agentd, new server.** No live frames arrive. A bound thread still
  answers successfully with readiness `unavailable`, the live stream opens
  normally, and final messages arrive through the existing route. Tested by
  `TestChatWithoutRelayIsUnavailableNotAnError`. The chat view will show this
  as **live view unavailable** once the composer slice ships; there is no chat
  UI yet.

The live endpoint returns `text/event-stream` and flushes each frame. Keepalives
arrive every 15 seconds; set proxy idle timeouts above 15 seconds. Each stream
ends after five minutes, so the client reconnects using its last cursor through
`Last-Event-ID` or `after`, following the resync rules above when replay expires.
Disable response buffering for event streams; the server sends
`X-Accel-Buffering: no`. Caddy automatically flushes event-stream responses and
needs no `flush_interval` setting for this route. Preserve authentication and
cursor headers, and do not cache the stream or log its response bodies.

## Acceptance and validation

`TestAgentdRelayReachesViewerAndPersistsFinalOnce` runs the agentd relay
against the real server routes. Each frame of the S1 fixture stream reaches an
SSE viewer within one second, and a final whose first response is lost is
retried with the same client ID and stored once.
`TestManagedChatSessionUsesLeaseBoundCurrentBinding` and the extended
`TestBindingRejectsForeignWrongRoleManagedAndStaleRegistration` check the
managed boundary and the binding lookup. A managed run with `chat` and the
right lease is admitted. A run without `chat`, a foreign principal, a wrong
lease, a stopped or stale session, a superseded epoch or a revoked key scope
gets 404. The relay's queue bound, backoff, rebinding, refusal handling and
receipt evidence are covered in `internal/agentd/chat_relay_test.go`.

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
require explicit resync without obsolete content.

No migration is needed. The registration request accepts one more capability
value, `chat`; pinned response schemas are unchanged.
