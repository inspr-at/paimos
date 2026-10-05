// SPDX-License-Identifier: AGPL-3.0-only

// Package events provides the tenant event writer, history, SSE and undo.
//
// Coordinator wiring: events.New(pool, relations.UndoOption()) returns an
// httpapi.Module; add it and relations.New(pool) to Server.Modules after auth.
// There are no background workers to start or stop. Each SSE request owns a
// dedicated LISTEN connection (outside the query pool); all durable replay runs
// through db.InTenant. Disconnects release the listener, and clients reconnect
// with Last-Event-ID. Notifications contain only a tenant and event ID.
// Live views open the stream with ?after=latest: it starts at the newest
// event, and each connection first sends stream.ready naming its resume ID.
// Reading attaches node_changes to node.* and knowledge.* events (id, project, changed
// attribute names, revision); nothing about them is stored.
// Knowledge learning decisions name their source node and its authorized
// project with a learning field and null revision, since the node is unchanged.
// Late harness.usage_reported events attach a projection hint for the current
// readable session binding, so spend can refresh after a session has stopped.
//
// Every resource mutation must call Append(ctx, tx, principal, Change{...})
// inside its existing db.InTenant callback, after taking resource locks. Append
// runs MutationGuard when one is registered. Never
// open a second transaction for the event. Writer implements the same Append
// method for dependency injection. Before/After must be full API snapshots;
// nil means absent. The DB allocates ordered IDs and notifies on commit.
//
// Undo is opt-in by stable event type via WithUndo. The owning package's
// UndoFunc locks and authorizes its resource, rejects stale snapshots, restores
// it, and returns the actual compensating Change without writing an event.
// The events module takes the tenant fence FOR NO KEY UPDATE and rechecks
// events.undo in that transaction, plus events.undo_other for another actor's
// event. It checks duplicate undo, runs the handler, and appends exactly one
// event in the same transaction. Access and rules writers that also need the
// tree advisory lock must take that tenant fence first, then tree and rows;
// the event counter is last.
// Unknown types and compensating events are not reversible (409). Register
// node/kind/view handlers from their owning packages when wiring those modules;
// their schema, tree and ownership rules must not be bypassed by generic SQL.
package events
