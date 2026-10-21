# Browser request refresh

Mounted project, ticket and lead views share queue and lead refreshes through their stores. Each watched resource has one refresh loop; the last consumer leaving stops it. Agents subscriptions and these refreshes share one browser event stream.

Relevant events are wake hints for authorized API reads. A fixed 400 ms window batches named event bursts; a hint during a read invalidates its answer and retains one catch-up read. Opening or recovering the shared stream refreshes immediately, so an invalidated initial read does not leave the first snapshot waiting for a batch timer. Queue, lead, plan and delivery projections reconcile every two minutes to recover missed events and changes driven by time. Existing session liveness, pending-operation and active-review checks retain their shorter intervals.

Periodic network reads pause in hidden or offline tabs and refresh when the tab or connection returns. Ticket readers cancel earlier reads on screen changes; store generations fence old resource and identity responses. Denied queue, lead, delivery and review reads remove previously authorized snapshots. Quote presence sends its idle transition when hidden, then suspends its periodic heartbeat; returning can renew an expired presence lease through the existing rejoin path.

Transient projection read failures retain the previous snapshot with error feedback. Access-change hints and transport recovery trigger fresh reads; event payloads never grant access.

This changes request scheduling without changing the UI or API contract. Retired Journey/Stage features and the r138 Goals and Decision Desk views are outside this change.
