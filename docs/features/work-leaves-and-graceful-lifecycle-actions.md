# Work leaves and graceful lifecycle actions

Agents bind to work leaves: only live work children make a parent. New dispatch,
claims, session registration/binding and work-order placement share the tree
fence with child creation. A busy leaf cannot gain work children; historical
sessions keep their original IDs and bindings.

Busy work does not prevent adding siblings to its project or existing work
parent. Coordinator live ETA writes accept work leaves, reject work parents,
and recheck current authority under the tree fence. Work leaves also retain
missing-estimate guidance and their saved dispatch model placement.

In a work item's **Work actions** sheet, **Split into children** saves up to
20 child titles and requests an AEON-479 cooperative handover before creating
them. **Cancel with its open children** previews up to 100 open leaves, cancels
idle leaves, and waits for running leaves to report stopped. Finished leaves
stay finished. These requests never send process signals or treat heartbeat loss
as a confirmed stop. The saved action continues after closing the sheet or
restarting the server, with the original person's current permissions checked
inside every write. A changed split target requires abandoning the saved request
and reviewing a fresh preview; abandoning keeps earlier completed writes and
handover requests intact. An unconfirmed closure with reason `heartbeat_lost`,
`archived_process_unknown`, `removed_process_unknown` or
`attach detached; process exit unconfirmed` retains its busy hold for two hours
from the later of `stopped_at` and `heartbeat_at`. A NULL heartbeat still gets
the full two hours. Confirmed stops release immediately; live sessions and unknown
reasons keep their holds. Expiry changes no closure, lease or historical binding
and never proves process exit. Bindings of an unconfirmed generation cannot
detach or move away while
its work action is pending. The same generation may revive and finish handover.
Cooperative wrap-up delivery expires after ten minutes. **Check handover** and
the background worker retry expired delivery under the original person's current
authority, with an audit trail, while retaining the fence. Retries never escalate
to force stop and reuse any still-active delivery.

After an uncertain stop, the owned executor can report a later confirmed exit
through `POST /api/projects/{projectId}/harness-sessions/{sessionId}/confirm-exit`
with its exact worker lease and current `harness.worker` authority. Only
`process_exited`, `process_failed` and `force_stopped` are accepted. Confirmation
releases the work fence, records `harness.stop_confirmed` once and preserves
closure timestamps, archive receipts and historical bindings. Archived servicing
remains revoked; the administrative recovery observation stays unchanged.
Work-order, harness and lifecycle action requests buffer bodies up to 1 MiB under a
ten-second network read deadline before opening their tenant transaction.
Failed reads retain the deadline so draining the body cannot stall indefinitely.
Stalled uploads cannot hold the work-tree or tenant fences; key scopes and
target permissions are still checked inside the final mutation transaction.
Coordinator registration and resume adopt up to 1000 direct live/paused children
atomically. Larger scopes return 409; buffered audit snapshots have a 32 MiB cap
(429 on excess), and flush only after all generation and child mutations finish.

The additive API is `GET/POST /api/nodes/{id}/work-lifecycle`,
`POST /api/nodes/{id}/work-lifecycle/{action}/continue`, and
`DELETE /api/nodes/{id}/work-lifecycle/{action}`. Requests bind a UUID, the node
revision and the preview's scope digest. Waiting and completed responses are
explicit; replaying a request creates no extra children or cancellation events.
Builder completion with a review range still commits during pending handover:
automatic review records `review.unavailable` and leaves the review gate closed,
without starting new work or preventing the original generation from finishing.
Work-order cancellation rechecks `work_orders.write` and queued reservation
release rechecks `run.create`, alongside `nodes.write` and session control.

A person's existing graceful work action also settles a stale active run when
all bound session holds have released, or when no generation is bound and the
last run activity is at least two hours old. Run creation, start and the latest
telemetry count as activity; fresh telemetry after closure keeps the hold.
A closure predating the run cannot release a new orphaned run immediately.
The write rechecks `run.create` and `work_orders.write` under the access/tree
fence, marks the run `ownership_lost`, and records `run.stale_hold_released`
with the action ID. A `work_lifecycle_release.exit_unconfirmed` trace marker
retains the writer fence even for runs without a durable assignment. Only a
finished exit report from the same authorized daemon clears it. It preserves
daemon ownership, launch uncertainty and all
capacity/accounting holds so a later owned executor can reconcile. It never
reports process exit or makes an uncertain writer eligible for takeover.
A standalone running order with no reporting generation or run retains a
two-hour hold from its last update and then settles through the same action,
with the existing `work_order.cancelled` audit. Replays do not duplicate events.

Busy cancellation errors name visible holders by session/run/work-order ID,
label and age in seconds. Each type shows up to 20 holders and identifies
truncation. Handover errors show at most two work targets. Titles, hostnames,
principal names, private notes, telemetry and leases are excluded; hidden project
holders retain a generic message.

Caller audit: `aeon_work_busy`, lifecycle completion and handover delivery use
`aeon_work_session_released` for expiring busy holds. The original immutable
`aeon_work_session_stopped` deliberately remains confirmed-exit proof for SQL
binding/rebinding guards, lead succession and worker-yield checkpoints, plan
capacity, writer admission, escalation and the confirm-exit endpoint. Those
safety gates must not accept age as evidence of process exit.

Integration seams: AEON-655's work surfaces can supply `is_leaf` to the shared
queue helper; this package also accepts the existing estimate's `is_parent`
projection and otherwise fails closed. The AEON-429 `work-parent-status` rollout
continues to govern derived parent states from AEON-650. No sibling package is
required for the lifecycle API or sheet. When the DSAR inventory lands, classify
`work_lifecycle_actions.children` as personal drafts and targets/results/requester
as tenant identifiers; that inventory is absent from this stacked checkout.
