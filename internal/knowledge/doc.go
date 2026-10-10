// SPDX-License-Identifier: AGPL-3.0-only

// Package knowledge is the human surface of the knowledge plane: runbooks,
// guidelines, memory, external systems and related projects that INSPR
// agents read before they work (AEON-126).
//
// Entries stay ordinary nodes, so imports, the paimos CLI verbs
// (knowledge list/get/create/update through /api/nodes), search and the
// event log keep working unchanged. Node kind slugs are snake_case
// (external_system); the wire type is the classic kebab-case URL segment
// (external-system). fields.slug names an entry for agents and fields.metadata
// keeps type-specific values (guideline rule, external system url, ...).
// Every other field is preserved on write.
//
// What this package adds over /api/nodes:
//   - GET /api/knowledge lists and searches entries across projects or in one
//     (project_id), with the nearest project, a plain-text excerpt around the
//     match, the last writer, link counts and type/status counts.
//   - GET /api/knowledge/{id} and GET /api/knowledge/resolve?project_id&type&slug
//     return one entry with its body, author and linked nodes. resolve follows
//     slug renames recorded in knowledge.updated and node.updated events and
//     reports renamed_from, so an old link or agent reference still lands.
//   - GET /api/knowledge/graph?project_id&types&status&include=tickets derives
//     a body-free graph (AEON-146): live knowledge, directed relations and
//     resolved wiki/Markdown/code-slug/key mentions. Same-kind slugs, including
//     rename history, take precedence over other kinds in the project. Typed
//     Markdown links stay in their named project/type. Direct ticket satellites
//     are optional and their bodies are never read. Directed pairs are unique,
//     relations win over mentions, and degree counts unique returned neighbours.
//     The response flags truncation at 2,000 nodes or 8,000 edges. This read-only
//     projection needs no migration, event write or separate plugin manifest;
//     knowledge.New already mounts it, without additional coordinator wiring.
//   - POST, PATCH and DELETE /api/knowledge[/{id}] enforce the slug rules of
//     classic Paimos (^[a-z][a-z0-9_-]*$, at most 64 characters, unique per
//     project and type among live entries; memory reserves references, stale,
//     proposed and needs-review) under a per-project advisory lock. PATCH and
//     DELETE honour If-Unmodified-Since (exact updated_at); a stale value
//     returns 412 with the current entry and writes nothing.
//   - Status is the classic trio: active (stored as backlog), proposed and
//     archived (stored as cancelled). Any other stored state reads as active and
//     is kept until the status class changes.
//   - GET /api/knowledge/learnings?project_id lists open method learnings for
//     one project (AEON-275): live tickets, tasks and epics whose tags include
//     process-learning, and comments on nodes in the project whose current text
//     contains that tag as its own word (an optional # is allowed). Decided
//     items stay out of the list. Accept (POST .../accept) is person-only: it
//     appends one dated line to the chosen entry's changelog section (the last
//     heading whose text contains "changelog", or a new Changelog section),
//     links the source, and appends knowledge.learning_accepted. Dismiss
//     (POST .../dismiss) is person-only and appends knowledge.learning_dismissed.
//     Draft (POST .../draft) is person-only: it appends one rule to a chosen
//     rule draft and records knowledge.learning_drafted. It does not publish.
//     Agents may tag candidates; they cannot accept, dismiss, draft or undo.
//     They may prepare the decision (AEON-788): PUT .../recommendation stores
//     one current recommendation per learning (accept with a target entry and
//     an optional lesson line, or dismiss with a reason) and appends
//     knowledge.learning_recommended. It applies nothing. The list returns it
//     on each item, marked stale when the learning's text changed since; a
//     person applies it through accept (optional lesson) and dismiss
//     (optional reason). The list holds 50; next_cursor continues it.
//     A daily tagger nominates closed tickets, review verdicts and incident
//     comments. It never accepts them. Merging the process-learning tag into
//     a ticket appends node.updated under that tenant's System principal, in
//     the same transaction as the tag. paimos serve starts it.
//   - Principals with a viewer or read-only role cannot write (403).
//
// Every write runs in db.InTenant and appends one event in the same
// transaction: knowledge.created, knowledge.updated, knowledge.deleted,
// knowledge.learning_accepted or knowledge.learning_dismissed. The first four
// carry complete node snapshots in the nodes package's JSON shape. Missing
// external_system or related_project kinds are created on first use with a
// kind.created event, like the CLI does. UndoHandlers makes each of them
// reversible through POST /api/events/{id}/undo: undo re-checks that the entry
// is exactly as the event left it and that the restored slug is still free,
// otherwise it answers 409. Undoing an accept or dismiss also removes the
// decision, and only a person may do it.
//
// Coordinator wiring (this package does not edit cmd/aeon):
//
//	knowledge.New(pool)                                   // add to Server.Modules
//	events.WithUndoHandlers(knowledge.UndoHandlers())     // add to events.New(...)
//
// Migration 0750 adds two partial indexes: live nodes by (parent, kind, slug)
// and knowledge/node update events by the slug they renamed away from.
package knowledge
