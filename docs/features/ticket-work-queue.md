# Ticket work queue

`aeon queue list`, `add <ticket>`, `remove <ticket>`, `move <ticket> <position>`,
`reset` and `next` manage ticket work on the existing agent run queue. People
need `run.create` in the ticket's project; coordinator agents need their live
coordinator role and scoped key. Plain workers cannot manage the queue.

New and Backlog become Open when queued; Blocked retains its place until
unblocked. Tickets need an estimate, acceptance criteria and a named blocker
when blocked. `aeon queue readiness <ticket>` returns missing fields and an
estimate suggestion. `POST /api/queue/{nodeId}/estimate` explicitly applies a
missing estimate; acceptance criteria and blockers use the existing ticket
edit/relation APIs. No acceptance evidence is invented.

Priority then FIFO is the default. Manual moves persist until Reset; arrivals
append during manual order. `add <ticket> --agent UUID --profile UUID` is the
advanced Start now path, first in that agent's separate line. Optional
`--account UUID` pins an account and never overrides its allowances. `next`
routes ready work; existing daemon reservation and fenced claim checks perform
pickup atomically, move the ticket to In progress and remove its queue marker.
Ticket API and CLI JSON include `queued: {position, by, at, …}` while waiting.
Capacity hours are advice, not a reservation or a hard cap.

The project queue and ticket drawer move work using the server's shared
positions, including Move to top of the displayed project queue. Readiness
accepts live blocker relations. A failed queue read exposes an error and Retry
even before any queue snapshot exists. Suggested releases prefer the server's
expected start; otherwise they use earlier visible work and parallel capacity.
The hover labels this local estimate: other projects, current runs and blocker
delays are not included. Unmeasured capacity or an unknown unblock time keeps
the suggestion empty.

On phones, the configurable project header keeps the queue action visible in
Compact and Comfortable while folding the activity timestamp. Expanding a
collapsed header restores the queue action.
