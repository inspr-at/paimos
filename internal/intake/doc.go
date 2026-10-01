// SPDX-License-Identifier: AGPL-3.0-only

// Package intake stores cited Aithema intake for one journey project (AEON-36).
//
// New returns an httpapi.Module. The coordinator mounts it on the API server;
// this package does not mount itself or wire the live Aithema plugin.
// Routes, under the /api server:
//
//	POST /projects/{projectId}/intake/sources
//	GET  /projects/{projectId}/intake
//	POST /projects/{projectId}/intake/transcript-turns
//	POST /projects/{projectId}/intake/drafts
//	POST /projects/{projectId}/intake/drafts/{draftId}/accept
//	POST /projects/{projectId}/intake/drafts/{draftId}/replace
//
// Sources, transcript turns, drafts, citations, ticket suggestions and
// acceptances are immutable. A proposal never updates a node. A person accepts
// one draft. A new brief becomes a memory node and a new requirement becomes a
// requirement node plus a journey_requirements row; ticket suggestions stay on
// the draft until agreement. An existing target is updated only when its latest
// event is still the draft's base_event_id, the node was not already accepted,
// and the current text was not written by a person. A later edit, a second
// draft, or a person's own text returns 409 and leaves both the edit and the
// proposal in place.
//
// Agents need a live key. Reads allow intake.read or intake.write. Source and
// turn writes need intake.write. Proposing a draft also needs a live R2 grant
// of intake.write on the project node; the grant is not an approval of the
// text. People accept drafts. Source labels, locators and transcript bodies
// stay out of the event payload. File bytes are not stored here: a file source
// is linked only when that file id is visible in this tenant's file storage.
//
// Every read and write runs inside db.InTenant. The row and its event commit
// in that same transaction via events.Append.
//
// AEON-463: optional typed extensions and the original document_bytes remain
// immutable text, outside metadata event payloads. The native envelope retains
// its required fields; document_bytes extracts the one submission candidate's
// map without implementing the separate Aithema plugin protocol. Unknown
// namespaces are retained. Aithema owns registry and canonical size validation;
// the native 1 MiB transport cap accommodates 64 KiB maps plus escaping.
// GET intake?node_id filters to accepted drafts for that node or its generating
// requirement, with empty sources and turns and unchanged read authorization.
// AEON-360: NewDelegated verifies P01 delegated JWTs per route. Reads require
// exact intake.read, writes exact intake.write. AuthorityStore locks tid/pid/sid,
// plugin/requester identities and auth_epoch in the intake transaction; writes
// also check current gen and an ephemeral grant bound to those same identities,
// generation and epoch. Every write checks authority after the project lock.
// Read polling intentionally allows a stale gen. Requester is derived from
// act.sub; clients cannot impersonate another person. Ordinary API keys retain
// their established grant behavior. Bearer credentials cannot accept drafts.
// The coordinator must wire outer auth/project permission checks and the P03
// generation store in AEON-P04; P02 proves this boundary with a test double.
//
// Replacement and acceptance share the project lock. Immutable replacement
// edges leave the old draft superseded, link the new draft by supersedes_draft_id,
// and preserve the original response bytes. Same key with changed request bytes,
// old draft or actor returns idempotency_conflict. Retries still check current
// authority before returning receipts. Superseded drafts cannot be accepted or
// replaced, and accepted drafts cannot be replaced. No bare withdrawal exists.
// All intake errors contain error and code; domain refusals use the catalogue
// in testdata/error-codes.json. Target refusals distinguish drift, an existing
// acceptance, absent recorded content and person-authored text.
package intake
