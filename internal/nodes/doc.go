// SPDX-License-Identifier: AGPL-3.0-only

// Package nodes defines the R1 contract for AEON's configurable work tree.
// A project is a node of the starter "project" kind: it participates in the
// same arbitrary-depth hierarchy, relations, search, history and move rules as
// every other node. A separate projects table would duplicate those rules.
// Kinds belong to tenants; their immutable slug, label, short key prefix, SVG
// icon name, allowed child slugs and JSON Schema for fields are configuration.
// NULL allowed_child_kinds permits any child; an empty array permits none.
// Slugs are immutable because parent rules refer to them. The API validates
// fields against the kind schema before writing; the database checks JSON shape.
//
// Node keys are unique within a tenant. Creation calls aeon_next_node_key for
// the requested prefix or kind default, in the same transaction as the node
// and event. Explicit import keys are preserved and
// advance that prefix's counter. IDs and keys never change; delete is soft.
// Position is a decimal sibling rank, with (position,id) as the tie break.
// Moves lock/validate the tree, including cycles and allowed child kinds.
// Narrowing a kind's allowed children affects new links; existing children
// remain movable and deletable so a changed rule cannot trap old tree rows.
// Opaque cursors bind filters and ordering; tree pages are flat depth-first
// entries, so any depth is representable without recursive response nesting.
// Relations are directed, except "relates" is symmetric and stored with the
// smaller UUID as source. Handlers normalize that pair before insert.
//
// Every mutation writes one event in its tenant transaction via db.InTenant.
// events.id is allocated by a transactional per-tenant counter so commit order
// agrees with SSE replay order. before/after are complete resource snapshots;
// undo appends one compensating event linked by undo_of, never edits history.
// NOTIFY carries only tenant and ID; the SSE module LISTENs and replays durable
// rows after Last-Event-ID on wakeup/reconnect, filtering by tenant. The tenant
// setting must be applied for each replay query, including on LISTEN workers.
// Search uses weighted German and English title/body tsvectors and 1536-wide
// halfvec embeddings. Node writes enqueue embedding jobs; a worker computes
// embeddings asynchronously, checks content_hash, then stores the vector and
// deletes the job in one tenant transaction. Queued jobs mask stale vectors. The
// aeon_search_nodes SQL function fuses lexical and vector ranks in one query
// with reciprocal rank fusion (k=60); NULL vector gives lexical-only results.
// Saved views store list filters, sort and columns; handlers enforce owner-only
// edits and own-or-shared reads in addition to tenant RLS. Import status is
// read-only in R1; the later ingestion worker updates import_jobs.
// R0 must use a non-superuser database role in service environments: even
// FORCE ROW LEVEL SECURITY does not constrain PostgreSQL superusers.
// scripts/test-r1-contract.sql runs the database contract checks and rolls back.
//
// Worker file ownership (AEON-16 handoff; no shared-file edits by builders):
//   - nodes/kinds/tree: internal/nodes/*.go except doc.go; owns /nodes and /kinds.
//   - relations/events/SSE: internal/relations/*.go, internal/events/*.go;
//     owns /relations, /events, /events/stream and /events/{eventId}/undo.
//   - search/embeddings: internal/search/*.go, internal/embedding/*.go;
//     owns /search and the asynchronous embedding queue.
//   - views/import status: internal/views/*.go, internal/imports/*.go;
//     owns /views and /imports.
//
// This contract worker owns api/openapi.yaml, migrations 0100-0103 and doc.go.
// The coordinator alone wires modules into cmd/aeon and reconciles later
// contract changes. Each builder consumes tenant.PrincipalFrom, db.InTenant,
// and httpapi.Module from R0; no builder edits those shared packages.
//
// New returns an httpapi.Module for /api/kinds, /api/nodes and
// GET /api/tickets/graph (AEON-196). The coordinator mounts that module; this
// package does not wire cmd/aeon and does not register a plugin manifest.
// Every mutation calls Writer.WriteEvent inside db.InTenant. The ticket graph
// is a read: one tenant transaction, no event, no ticket bodies. A nil Writer
// selects SQLWriter, which inserts the event row until internal/events exposes
// its writer.
package nodes
