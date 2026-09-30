// SPDX-License-Identifier: AGPL-3.0-only

// Package importer copies the read-only classic Paimos API snapshot to Aeon.
// The importer never changes the source. Mapping, field by field:
//
//   - Project id -> node key PRJ-<id>; name -> title; description -> body;
//     status -> state; created_at/updated_at -> node timestamps. Product owner
//     resolves to fields.product_owner when its principal exists. Tags are in
//     fields.tags (R1 has no tag relation). Every fetched noncomputed project
//     value is also copied verbatim to fields.classic.<name>.
//   - Issue id and issue_key identify the node; type -> kind; title -> title;
//     description or knowledge body -> body; status -> state; timestamps ->
//     node timestamps; project_id/parent_id and parent relation -> parent node.
//     fields.acceptance_criteria and fields.notes retain Markdown for the
//     sidebar. fields.priority, tags, estimate_hours, estimate_lp, budget_hours,
//     total_budget, start_date, end_date, release, sprint_ids, needs_review,
//     archived, and accepted_at hold the corresponding source values. The
//     assignee_id, created_by and accepted_by user IDs resolve to principal
//     UUIDs in fields.assignee, fields.created_by and fields.accepted_by.
//     Unresolved source IDs remain in fields.classic. Every fetched issue
//     value is also copied verbatim to fields.classic.<name>, including other
//     billing, time, Jira, metadata and historical fields. The source URL hash
//     is kept in fields.classic.source_id for safe reruns. An existing node
//     whose kind differs from the imported type is a per-row conflict
//     (kind_change_not_allowed, with the current and requested kind). That
//     row is left unchanged, and the other rows in the snapshot continue.
//   - Project knowledge is merged with its issue record, then imported with
//     its original issue key. Orphan sprints become root nodes. Trash issues
//     are fetched, and deleted_at is retained in fields.classic.deleted_at.
//   - Relations become native parent, blocks, relates, duplicates and cites
//     links where possible. related, follows_from, impacts, applies_to_memory
//     and groups become relates. depends_on becomes blocks with the dependency
//     blocking the dependent. blocks keeps source as the blocker. A release
//     row is release membership: the classic container is the release and the
//     other end is the member issue (either column order is accepted when the
//     node kinds show which end is the release). Membership registers that
//     release on journey_releases for its project and, when the member is a
//     ticket, sets journey_tickets.release_node_id. It does not choose
//     journey_projects.current_release_node_id and does not invent a released
//     state. The classic type stays on the import.relation payload as
//     record.type and classic_type. Every relation, including unsupported
//     types and out-of-scope targets, is retained in an import.relation event.
//     Comments, history and attachment metadata are retained verbatim in
//     import events.
//   - Users become classic identities and tenant principals. User id,
//     username, display_name/name/first_name/last_name, email, role and created_at
//     are used. Principal names retain the classic username; principal email
//     supports explicit identity matching. Linked users resolve to canonical
//     principals for future assignments, while fields.classic stays unchanged.
//     User preferences stay
//     skipped. Passwords, keys, sessions and TOTP secrets are never requested.
//
// Intentionally skipped: project active_issue_count, done_issue_count,
// issue_count, open_issue_count, effective_rate_hourly, effective_rate_lp,
// last_activity, node_depth and rate_inherited, all derived/computed values;
// user fields status, nickname, avatar_path,
// markdown_default, monospace_fields, recent_projects_limit,
// internal_rate_hourly, show_alt_unit_table, show_alt_unit_detail, locale,
// recent_timers_limit, timezone, preview_hover_delay,
// issue_auto_refresh_enabled, issue_auto_refresh_interval_seconds,
// search_scope_shortcut, command_palette_shortcut,
// intake_confidence_threshold, last_login_at, totp_enabled,
// accruals_stats_enabled, accruals_extra_statuses and is_super_admin;
// attachment file bytes (only metadata is fetched); and nonproject user and
// instance memory, which the project knowledge endpoint does not enumerate.
// These skips are not reported as unmapped_fields. All fetched work fields
// survive either in node fields or in import event payloads. Dry runs perform
// the same source reads and return an empty unmapped_fields array. Source GETs
// are capped at four concurrently by default; --concurrency and --delay can
// lower pressure on classic PPM. A project whose issues or knowledge endpoint
// returns 404 is skipped. An issue whose detail endpoint returns 404 is also
// skipped. The report lists each skipped item with its type, source ID, request
// path and HTTP status; counts includes skipped, skipped_projects and
// skipped_issues. Other source HTTP errors abort the import.
//
// Reruns update nodes only when imported content changes and deduplicate
// auxiliary events. Native writes are serialized per tenant and source with a
// transaction advisory lock and use events.Append inside db.InTenant. After a
// successful write, the importer refreshes statistics on its touched tables so
// large snapshots are queryable without waiting for autovacuum.
//
// BackfillRelations replays import.relation events already stored for one
// tenant and applies the mapping above. It is the function the coordinator
// calls from cmd/aeon; this package does not register a command.
//
//	paimos import backfill-relations --tenant SLUG
//
// Resolve SLUG to tenants.id, open the pool with db.Open, then:
//
//	report, err := importer.BackfillRelations(ctx, pool, tenantID)
//	json.NewEncoder(stdout).Encode(report)
//
// report.Writes is the number of projection rows inserted or updated.
// A second successful call returns Writes == 0 and appends no events.
//
// For PMA, the coordinator can wire a separate operator command backed by
// NewPMAAdapter and PostgresWriter (this package does not edit cmd/aeon):
//
//	paimos import pma --source-instance NAME --source-url URL \
//	    --api-key-file FILE --tenant augmentoring [--project KEY] \
//	    [--dry-run] [--concurrency N] [--delay DURATION]
//
// Source access and target tenant must be confirmed by the operator before
// running a live import. Dry-run still reads the source but writes nothing.
// The source ID combines the explicit instance name and a source URL digest;
// records with the same classic key from a different source ID are rejected
// in one tenant, while the same numeric ID remains independent in another.
//
// QP8's PlanQuoteCRMMapping is an offline specification over synthetic
// customer/contact/quote records. It validates source-instance namespaces,
// links, numbers and integer minor-unit money, and flags missing original
// document/PDF evidence. It is deliberately separate from Source and Writer:
// the current PMA API snapshot does not include these records or acceptance
// receipts. The coordinator cannot wire a CRM/quote import from this plan.
// Any future adapter must use a confirmed read-only source API, persist rows
// inside db.InTenant with an event for every change, and preserve original
// accepted evidence instead of synthesizing it in Aeon.
//
// B3 repairs: future imports normalize known state spellings and persist safe
// classic user presentation fields in import.user_* events. The durable mapping
// remains identities(issuer=paimos-classic, subject=source_id:classic_user_id)
// joined to a tenant principal; partial imports reuse it. Display names prefer
// display_name/full_name/name, then first+last name, then username.
// BackfillPrincipals(ctx, pool, tenantID) repairs missing native assignments and
// recoverable display names from retained users, with one event per mutation.
// The coordinator wires this operator action; no new HTTP module or plugin is
// required. Old imports that retained only a username cannot reconstruct a full
// name: a fresh user snapshot with a display name is needed. No live source is
// contacted by the backfill. Migration 0531 normalizes existing states atomically.
//
// CB3 cutover verification (AEON-117): the coordinator wires the CLI verbs;
// this package does not edit cmd/aeon. For `paimos import reconcile --source-url
// URL --api-key-file FILE --tenant SLUG`, construct an HTTPSource, configure
// its GET request cap/delay, open the target pool, and call
// Reconcile(ctx, source, pool, attachments.Store{FilesDir: cfg.FilesDir}, slug,
// projectKey). Reconcile prints progress every 100 completed items to stderr,
// and writes one explicitly partial JSON report to stdout on SIGINT/SIGTERM
// before returning the cancellation error. ReconcileWithOptions accepts
// progress/partial writers and an interval for callers that manage output.
// JSON-encode a successful ReconcileReport and print Summary as the short text
// result. Reconcile reads classic projects, issues, comments, relations,
// attachment bytes, knowledge and user links; it reads actual Aeon attachment
// bytes through Store.Open. Per-project categories include counts, aggregate
// checksums, and missing/extra/changed classic IDs. Unreadable classic project,
// issue and attachment items are skipped findings with kind, classic ID and
// HTTP reason; skipped_count and Summary expose their total. A skipped item is
// excluded from differences, so it cannot create a false extra target item.
// A partial report has progress and findings but no unverified differences.
// Authentication, network, malformed data and target read failures still abort.
// Reconcile never writes either system and classic requests are GET only.
//
// `paimos import paimos` can invoke Importer.RunDelta for the final delta. It
// scans a complete classic GET snapshot because several classic record types
// lack trustworthy update timestamps; writer provenance comparisons apply
// only new or changed records. Import events are append-only, repeated runs
// are idempotent, and an Aeon edit after the prior import is reported in
// Report.Conflicts instead of being overwritten. A conflict needs operator
// resolution before cutover; the importer does not silently pick a winner.
// RunDeltaSnapshot returns that source snapshot. Call ImportAttachmentDelta
// with it, the same tenant/actor IDs and Store to refresh changed classic
// bytes and copy new files. Its report separately lists attachment conflicts. Existing
// ImportAttachments retains its original skip-existing behavior.
package importer
