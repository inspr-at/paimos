<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Chat live stream and final outbox

The opt-in chat module adds `GET /api/chat-threads/{id}/live`. Two viewers of
the same person's conversation receive the same normalized agent updates.
Every frame and heartbeat checks current participant and project access.
Interim text and tool activity stay in bounded RAM; they never enter the
database, audit events, telemetry or logs.

Reconnect with `Last-Event-ID` or `after`. Replay resumes strictly after that
caller/thread-scoped cursor. Each conversation holds at most 64 frames and
256 KiB; the server admits at most 256 buffers, 256 viewers and four viewers
per conversation. Idle buffers expire after two minutes on subsequent relay
activity. Restart or a replay gap returns HTTP 409 before streaming, or sends
`resync` and closes an existing stream. Reload final history before reconnecting
without a cursor. Streams rotate after five minutes; writes have a three-second
deadline. Replay is a transient convenience, not a durable transcript.

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
