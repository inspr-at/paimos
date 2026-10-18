# Session chat pickup

A person's durable chat to an exact harness session stays queued when no hook
or listener is running. Missing listeners and elapsed delivery deadlines do
not turn it into a failed message. Session end and actual handoff failures keep
their existing failure behavior, including an explicit sender expiry; attached volatile messages use their own
protocol. Previously failed messages are not resent automatically.

Chat shows “Waiting for the session to pick it up”. After ten minutes without
first pickup, the sender receives one System notice. The same
`delivery.stall_alerted` event channel used by Decision Desk replies alerts the
coordinator, with session owner/lead routing metadata and no message body. The
existing chat status tooltip also explains the stall, without moving controls.
The message remains available for pickup.

`GET/PUT /api/settings/inbox-delivery` exposes
`chat_pickup_stall_seconds` (60–86400, default 600). Omitting it on PUT preserves
the saved threshold. Changes apply to chats still awaiting first pickup.

Hooks and listeners read in sent-event order with bounded pages. A two-minute
pickup lease excludes competing readers, including a harness drain. If a
response is lost before acknowledgement, the same message IDs can be replayed
after the lease; consumers retain their existing ID-based deduplication and
acknowledgement protocol. Pickup and confirmation events are recorded once.
The receipt reaches Read after recipient acknowledgement.
Decision Desk replies keep their existing replay behavior and thirty-minute
stall threshold.
