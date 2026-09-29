// SPDX-License-Identifier: AGPL-3.0-only

// Package activity supplies B2's core ticket timeline and native comments.
// The coordinator mounts activity.New(pool) alongside nodes.New; there is no
// cmd/aeon wiring here. This is a core node API, not an installable business
// plugin, so it requires no plugin manifest or builtin registry change.
//
// GET /api/nodes/{nodeId}/activity returns {items,next_cursor}. Item IDs are
// decimal strings naming tenant-local creation/history events. Items sort by
// (at,event ID) descending. Cursors bind tenant, node, the last ordering tuple
// and an event watermark; later writes/backdated imports do not enter that
// pagination session. Refresh without a cursor to see newer comment revisions.
// This is a live name projection, not a historical principal-name snapshot.
//
// Imported history is compared in ascending source timestamp/event-ID order,
// independently per source instance. The first snapshot establishes a baseline
// rather than inventing previous values. Only status, priority, assignee, title
// and parent differences survive on an import; native state becomes product
// field status. A native node update also keeps a labels change: fields.tags,
// names in stored order, including a tag written by the System principal.
// Imported creation uses the source creation time rather than ingestion time.
// Re-imported comment revisions retain their first event ID and position.
// Classic authors resolve through paimos-classic identities and tenant-local
// principals, resolving linked aliases to the target ID and display name.
// Native comment writes use the canonical person and preserve ownership of
// older comments through the same link. Missing mappings preserve the source author name with a null ID.
// Assignees resolve to principal names; parent values retain source IDs/native
// UUIDs. Markdown remains unrendered and consumers must render it safely.
//
// POST comments returns 201 with a comment item. PATCH/DELETE on
// /api/nodes/{nodeId}/comments/{commentId} address the comment.created event ID.
// Only its author can mutate it strictly before 15 minutes from creation, with
// no admin override. Each operation appends comment.created, comment.updated
// or comment.deleted via events.Append in db.InTenant. Edits/deletions serialize
// by comment before reading state, then check the database clock. Edits retain
// creation time; deletions disappear from the timeline but retain audit events.
// Imported comments are read-only. Authentication comes from the host's usual
// tenant middleware, like the other node APIs. Every database query, including
// timeline and identity reads, runs inside db.InTenant; no schema is added.
package activity
