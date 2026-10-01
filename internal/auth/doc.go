// SPDX-License-Identifier: AGPL-3.0-only

// Package auth authenticates people and agent keys. The middleware applies an
// outer, deny-by-default key-scope ceiling before any module handler runs.
// Module handlers still check principal kind, resource ownership and live grants.
// GET /api/me emits Aeon-Contract: me/1.0 without changing the strict JSON
// body. Additive optional response fields require a minor bump; breaking
// changes require a major bump. internal/reportercontract pins its schema.
// Scope management rechecks keys.manage under the tenant/key lock. A confirmed
// role_extension additionally requires roles.manage, the current custom agent
// workspace role and permissions held by editor and original creator. Role and
// key changes share one transaction and one audit event. Management reads and
// edits prune unknown stored scopes with audit evidence; known scopes stay.
//
// KX1 key expiry/rotation uses the existing New(cfg, pool) httpapi.Module;
// no new plugin manifest or coordinator wiring is needed. POST /api/agent-keys
// accepts rotate_key_id plus an optional future expires_at after confirmation.
// The replacement keeps the old agent, label and scopes, subject to the actor's
// current grants and agent role rules. Creation and revocation share one
// db.InTenant transaction and append safe before/after audit snapshots. A
// revoked key cannot be rotated twice; an expired key may be replaced. Only
// the successful response carries the replacement secret. No timer rotates
// keys and no event, list response or snapshot includes secrets or hashes.
//
// Agent route-to-scope table (GET and HEAD are reads unless noted):
//
//	/api/projects, /api/nodes, /api/node-keys, /api/kinds, GET /api/tickets/graph:
//	  nodes.read; node writes use nodes.write; kind and tag writes use
//	  nodes.configure. Kind mutations and tag rename/delete require a person
//	  admin; other tag metadata edits retain the member route.
//	/api/nodes/{id}/activity|comments|attachments, /api/attachments:
//	  nodes.read or nodes.write.
//	/api/relations: relations.read or relations.write.
//	/api/events: events.read or events.undo.
//	/api/search: search.read.
//	/api/views, /api/preferences, /api/project-groups:
//	  views.read or views.write.
//	/api/knowledge: knowledge.read or knowledge.write.
//	/api/projects/{id}/journey|requirements|releases:
//	  journey.read for reads; agent writes have no mapping.
//	/api/approvals: approvals.read for list, approvals.request for proposal;
//	  decisions and revocations have no agent mapping.
//	/api/inbox, /api/projects/{id}/messages|message-targets|message-deliveries:
//	  inbox.read or inbox.send. GET /api/inbox/messages/{id}/receipt is
//	  inbox.receipt.
//	/api/models, /api/plugins: models.read or plugins.read; writes have no
//	  agent mapping. A coordinator key may resolve models even when its stored
//	  scopes were minted before models.read, while its live coordinator
//	  role grants remain in place and its creator permits that read.
//	/api/rules: rules.read or rules.write. Publishing and restoring have no
//	  agent mapping. A coordinator key's rules.read applies on projects it can
//	  already read after the creator intersection, not as a workspace
//	  permission. Operator-created keys without a human creator are refused
//	  by the rules handler with rules_owner_required; issue the key as a
//	  signed-in person to preserve personal rule ownership and merge context.
//	/api/work-orders: work_orders.read or work_orders.write; run creation
//	  uses run.create. /api/runs: run.read, run.claim or run.telemetry.
//	/api/harness-sessions, /api/projects/{id}/harness-sessions:
//	  harness.read, harness.write, harness.worker or harness.control.
//	/api/projects/{id}/intake: intake.read or intake.write.
//	/api/stage-handoffs, /api/projects/{id}/baseline-batches:
//	  stage.<op>; the handler rechecks the exact operation and grant.
//	/api/agent-accounts, GET /api/me: account.manage.
//	/api/time-entries, /api/time-periods, /api/nodes/{id}/time-totals:
//	  hours.read or hours.write.
//
// Every other API path returns 403 to an authenticated agent key, including
// business/customer/admin and public-capability paths when a key is presented.
// Empty scope lists grant nothing. Anonymous public calls retain their normal
// behavior. Person sessions are governed by role and module checks.
package auth
