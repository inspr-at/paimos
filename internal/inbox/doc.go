// SPDX-License-Identifier: AGPL-3.0-only

// Package inbox is AEON's durable principal inbox (AEON-27, migration 0200).
//
// Coordinator wiring — this package does not edit cmd/aeon:
//
//	srv.Modules = append(srv.Modules, inbox.New(pool))
//	go inbox.NewWorker(pool, inbox.WorkerOptions{}).Run(ctx)
//
// New returns an httpapi.Module for /api/inbox/messages, /api/inbox/stream,
// /api/inbox/targets and GET /api/inbox/messages/{messageId}/receipt.
// NewWorker posts webhook wake hints. Both use the server pool. Auth
// middleware must leave the bearer token on the request: agent sends are
// allowed only when that token's agent_keys row includes inbox.send. The
// receipt route is inbox.receipt (agent-grantable). Person sessions are not
// scope-gated. MessagingPlugin remains the plugin manifest; the receipt does
// not add another one. The coordinator mounts New and MessagingPlugin and
// does not edit this package to wire them.
//
// The receipt is the sender's proof of hand-off. Only that sender principal
// can read it; every other principal, including a tenant admin, gets 404,
// whether or not the message exists. Acceptance records state queued.
// handed_off is recorded only when the receiver adapter confirms: a
// grok_bot_routine webhook 2xx completed by the routine dispatcher, or a
// local adapter's delivery-complete. A terminal adapter failure records
// failed and failure_reason. handed_off and failed never move backward, and
// handed_off_at is set once, in RFC3339. A webhook wake is not confirmation.
// POST /api/inbox/messages with the same idempotency key and body from the
// same sender returns the original message id.
//
// Project messaging wiring: mount NewMessaging(pool, encryptionKey) alongside
// New and register MessagingPlugin in the coordinator's compiled registry.
// Existing migrations suffice for B7; api/openapi.yaml defines the additions.
// GET /projects/{projectId}/messages is person admin/super_admin inspection:
// limit 1..200, newest_first, address (or to), thread and pending filters.
// The reply_to chain selects the whole thread rooted at any supplied member;
// all recursive steps remain project- and tenant-scoped. Ascending sent-event
// ordering stays the default. In descending mode after is an exclusive upper
// bound (zero starts at newest). Every message now includes created_at and a
// nullable human_resolution_outcome. Recipient listen retains its ten-row cap.
//
// POST /projects/{projectId}/messages/{messageId}/resolution accepts decision
// resolved|dismissed and optional note. Only authenticated person admins or
// super_admins may call it; bearer authorization and agent attribution are
// rejected. inbox.action_resolved is the immutable resolution projection,
// with actor attribution and a digest of the note (no note text in events).
// Same decision/note replays the first response; different material conflicts.
// The transaction uses messaging's tenant advisory lock before message locks
// and event insertion. pending=true omits any resolved/dismissed held item;
// full inspection retains it, still held. Resolution never releases a message,
// sends a delivery, acknowledges it, or closes an explicit reply obligation.
//
// Session-bound messages belong to the exact generation. Sending, listening,
// and acknowledging return 409 session_ended after stop or archive, including
// acknowledgment retries. Ending a generation atomically closes its open reply
// obligations and appends inbox.reply_obligation_closed with message_id and
// closed_reason=session_ended, attributed to System. No reply is fabricated.
//
// AEON-280 delivery guarantee: a message to an agent is delivered or its
// sender is told loudly. Every new queued receipt carries deliver_by (session
// 5 min, otherwise 30 min, tenant-configurable at /api/settings/inbox-delivery
// with the adapter attempt cap, default 8). paimos serve runs NewSweeper(pool);
// a session advisory lock keeps it a single runner. Past the deadline, over
// the cap, or when the bound session ends (FailSessionMessages, called by the
// harness in the stop transaction) the delivery goes dead, the receipt fails
// (deadline|attempts|session_ended|no_listener), the message leaves every read
// path, inbox.delivery_failed is appended and System writes one notice to the
// sender plus a copy to the recipient session's coordinator. Session pulls,
// drains, streams and acks record inbox_seen_at/via on the session; a first
// hand-over stamps fetched_at (Delivered) and appends inbox.message_fetched.
// GET /api/inbox/message-status gives the sender sent|delivered|read|
// not_delivered per message.
//
// Every inbox read and write runs inside db.InTenant. The worker's tenant
// list is the one exception, because tenants has no tenant_id and no RLS
// (the same registry read as the embedding worker). Mutation events commit
// in the same transaction as their state changes. Event
// snapshots and webhook bodies never include the message text. A webhook
// POST is only {"message_id","event_id"} and is never authority; listen and
// SSE replay unacked rows after the sent event id. NOTIFY on aeon_events is
// only a wake hint. Long polls hold at most 30 seconds. Webhook URLs are
// checked again on every delivery: public HTTPS, no loopback, private,
// link-local, metadata, mixed-record or redirect targets.
package inbox
