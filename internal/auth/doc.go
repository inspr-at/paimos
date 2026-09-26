// SPDX-License-Identifier: AGPL-3.0-only

// Package auth authenticates people and agent keys. The middleware applies an
// outer, deny-by-default key-scope ceiling before any module handler runs.
// Module handlers still check principal kind, resource ownership and live grants.
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
//	  agent mapping.
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
