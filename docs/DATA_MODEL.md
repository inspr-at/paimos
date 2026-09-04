# PAIMOS Data Model

**Status**: Current with known maintenance follow-up (active API schema `2.3.0`)
**Last verified**: 2026-08-27 against `backend/db/db.go`, `backend/handlers/schema.go`, and live ppm `/api/schema` (`5.18.0`, schema `2.3.0`)
**Schema source of truth**: `backend/db/db.go` — migrations run in order on startup.
**Legacy**: `docs/archive/DATA_MODEL.md` captures the v0.3.5 pre-release baseline and is kept for archival reference only.

> This is the canonical data-model document. The v1.0.0 / v1.1.1
> releases added `project_members`, `access_audit`, `time_entries`,
> `attachments`, `issue_relations`, sprints, etc.; those are documented
> below alongside the original v1→v2 structural changes.

---

## Core Concept

The entity hierarchy changes from a strict tree to a **mixed model**:

- Groups/Sprints → Tickets use **M:N relations** (a ticket can belong to multiple groups and sprints)
- Tickets → Tasks keep **strict 1:1 parent** (unchanged)

Group types (Epic, Cost Unit, Release) and Sprint are **different views into the same set of tickets**, not separate containers. All live in the `issues` table with a `type` discriminator.

---

## Entity Hierarchy

```
PROJECT
  │
  ├──1:N──► GROUP (type = epic | cost_unit | release)
  │           │
  │           │  M:N via issue_relations (type = 'groups')
  │           │
  │           ▼
  │         TICKET ◄── M:N via issue_relations (type = 'sprint') ──► SPRINT
  │           │
  │           │  1:N strict (parent_id)
  │           │
  │           ▼
  │         TASK
  │
  │         issue_relations also handles:
  │           type = 'depends_on'  (ticket/task → ticket/task)
  │           type = 'impacts'     (ticket/task → ticket/task)
  │
  ├──1:N──► TIME_ENTRIES   (on tickets, per user, start/stop tracking)
  ├──1:N──► COMMENTS       (on any issue)
  ├──1:N──► ISSUE_HISTORY  (on any issue)
  └──M:N──► TAGS           (on project or any issue)
```

---

## Resolved Design Decisions

### Single `issues` table with type discriminator

All entity types live in one `issues` table. Reasons:

- Shared behavior: key generation, tags, comments, search, history, CSV, CRUD lifecycle
- No duplicated handler/component logic
- Sparse nullable columns have zero performance cost at this scale
- `type` field discriminates; frontend renders different views/columns per type

### `issue_relations` — one table for all relationships

Replaces the current `parent_id` for group→ticket links **and** the free-text `depends_on`/`impacts` fields.

```sql
issue_relations (
    source_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    target_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    PRIMARY KEY (source_id, target_id, type)
)
```

| Relation `type` | `source_id` | `target_id` | Meaning |
|-----------------|-------------|-------------|---------|
| `groups` | group (epic/cost_unit/release) | ticket | Ticket belongs to this group |
| `sprint` | sprint | ticket | Ticket is in this sprint |
| `depends_on` | any issue | any issue | Source depends on target |
| `impacts` | any issue | any issue | Source impacts target |
| `follows_from` | any issue | any issue | Source is a sequel/follow-up of target |
| `blocks` | any issue | any issue | Source blocks target's progress |
| `related` | any issue | any issue | Loose "see also" link with no causal direction |
| `applies_to_memory` | ticket | memory entry | Ticket's lesson surfaces against this memory (PAI-342 / M97) |

- A ticket can have 0..N group relations of different group types
- A ticket can be in 0..N sprints (tickets flow between sprints)
- Dependency/impact links work between any issue types
- Application logic enforces type constraints where needed

### Strict 1:1 for ticket→task via `parent_id`

Tasks always have exactly one ticket parent. This stays on `issues.parent_id`, unchanged from today.

### Free-text fields that go away

| Field | Replaced by |
|-------|------------|
| `cost_unit` (free-text on issues) | Relation to a `cost_unit` group entity via `issue_relations` |
| `release` (free-text on issues) | Relation to a `release` group entity via `issue_relations` |
| `depends_on` (free-text on issues) | `issue_relations` with `type = 'depends_on'` |
| `impacts` (free-text on issues) | `issue_relations` with `type = 'impacts'` |

---

## Expanded `type` Values

| Type | Level | Description |
|------|-------|-------------|
| `epic` | Group | Feature grouping; has billing/budget fields |
| `cost_unit` | Group | Billing/accounting grouping; has billing/budget fields |
| `release` | Group | Version/release grouping; has dates and release state |
| `sprint` | Sprint | Time-boxed iteration; tickets flow in/out between sprints |
| `ticket` | Ticket | Work item; can belong to multiple groups and sprints |
| `task` | Task | Sub-item of a ticket; strict single parent |
| `memory` | Knowledge | Reusable lesson / rule the agents must follow (PAI-338) |
| `runbook` | Knowledge | Operator playbook for a known scenario |
| `external_system` | Knowledge | Pointer to an external service the project depends on |
| `related_project` | Knowledge | Cross-project reference card |
| `guideline` | Knowledge | Soft convention that isn't a hard rule |

The knowledge types share the issue infrastructure — history, comments, tags, FTS, soft-delete, undo all work the same. They differ via the `category_metadata` JSON column and the `slug` lookup key (see "Knowledge plane + agent attribution" below).

---

## New and Changed Fields on `issues`

### Group-level fields (nullable; only meaningful when type is a group type)

| Field | Type | Applies to | Notes |
|-------|------|------------|-------|
| `billing_type` | TEXT | epic, cost_unit | Enum: `time_and_material`, `fixed_price` |
| `total_budget` | REAL | epic, cost_unit | Currency amount |
| `rate_hourly` | REAL | epic, cost_unit | €/h |
| `rate_package` | REAL | epic, cost_unit | €/P (package rate) |
| `start_date` | TEXT | release, sprint | ISO date |
| `end_date` | TEXT | release, sprint | ISO date |
| `group_state` | TEXT | release | `unreleased` / `released` |
| `sprint_state` | TEXT | sprint | `planned` / `active` / `complete` |
| `jira_id` | TEXT | epic, cost_unit, sprint | External Jira ID for mapping |
| `jira_version` | TEXT | release | External Jira version for mapping |
| `pharos_request_id` | TEXT | any issue | Optional opaque Pharos host-action/request ID; empty means unlinked. Syntax-constrained and secret-rejecting; no remote lookup or provisioning side effect. |

### Fields that go away

| Field | Replaced by |
|-------|------------|
| `cost_unit` (free-text) | Relation to `cost_unit` group entity |
| `release` (free-text) | Relation to `release` group entity |
| `depends_on` (free-text) | `issue_relations` with `type = 'depends_on'` |
| `impacts` (free-text) | `issue_relations` with `type = 'impacts'` |

### Fields that stay unchanged

title, description, acceptance_criteria, notes, priority, assignee_id, created_at, updated_at, issue_number/issue_key.

### `report_summary` — added in v3.5.0 (PAI-418)

| Field | Type | Notes |
|-------|------|-------|
| `report_summary` | TEXT NOT NULL DEFAULT '' | Customer-facing Projektbericht copy. Populated by two AI actions (`customer_rewrite`, `exec_summary`); read by the PDF endpoint when `text_source=report`. One field, two style options at generation time — the audience orientation is per-customer, not per-ticket. |

Indexes: covered by the existing LIKE / FTS coverage on issue body fields (`backend/handlers/issues_list.go`).

### Soft-delete (`deleted_at` / `deleted_by`) — added in v1.1.2

| Field | Type | Notes |
|-------|------|-------|
| `deleted_at` | TEXT NULL | ISO timestamp. `NULL` = live, non-NULL = in the Trash. |
| `deleted_by` | INTEGER NULL | `users.id` of whoever moved the row to Trash (plain integer, no FK — stale id after a user purge is acceptable; shown for display only). |

Index: `idx_issues_deleted_at` on `deleted_at`.

**Semantics:**
- `DELETE /api/issues/{id}` stamps `deleted_at` + `deleted_by` and cascades the stamp to every descendant reachable via `parent_id` (so tasks under a trashed ticket disappear alongside the ticket).
- `issue_relations` rows are **not** touched on soft-delete — a trashed ticket keeps its `groups` / `sprint` / `depends_on` / `impacts` links, so restoring re-attaches automatically.
- Every user-facing list / search / tree / report query filters `deleted_at IS NULL`. Trashed rows only appear via `GET /api/issues/trash` (admin-only).
- `POST /api/issues/{id}/restore` clears `deleted_at` on that row alone — cascaded children stay trashed (restore is deliberately explicit).
- `DELETE /api/issues/{id}/purge` hard-deletes a trashed row (and its cascade-bound rows: comments, history, tags, time_entries, attachments, issue_relations). Only works when already trashed, so the UI flow is always two-step.

### `parent_id` behavior change

| Relationship | Before (v1) | After (v2) |
|-------------|-------------|-------------|
| epic → ticket | `parent_id` | `issue_relations` (type=groups, M:N) |
| cost_unit → ticket | free-text string | `issue_relations` (type=groups, M:N) |
| release → ticket | free-text string | `issue_relations` (type=groups, M:N) |
| sprint → ticket | n/a | `issue_relations` (type=sprint, M:N) |
| ticket → task | `parent_id` | `parent_id` (unchanged, strict 1:1) |
| depends_on | free-text issue keys | `issue_relations` (type=depends_on) |
| impacts | free-text issue keys | `issue_relations` (type=impacts) |

`parent_id` remains on `issues` but is now only used for the task→ticket relationship.

---

## Unified Status Model

All issue types share one status enum. The enum grew beyond the
original v2-rename plan to cover the full **billing lifecycle** needed
by cost-unit / release reporting. Current CHECK constraint (source of
truth: `backend/db/db.go`):

```
CHECK(status IN (
    'new','backlog','in-progress','qa','done',
    'delivered','accepted','invoiced','cancelled'
))
```

| Status         | Meaning                                                    |
| -------------- | ---------------------------------------------------------- |
| `new`          | Just created; not yet triaged.                             |
| `backlog`      | Triaged, not yet started. (renamed from v1 `open`)         |
| `in-progress`  | Actively being worked.                                     |
| `qa`           | Work done; under review / quality check.                   |
| `done`         | QA passed; ready for delivery. (renamed from v1 `done`)    |
| `delivered`    | Shipped to customer / stakeholder.                         |
| `accepted`     | Customer / PO has signed off.                              |
| `invoiced`     | Billed to customer (final lifecycle state).                |
| `cancelled`    | Will not be done. (renamed from v1 `closed`; note double-L)|

Migration history: v1→v2 renamed `open→backlog`, `done→complete`,
`closed→canceled`; a later migration expanded the enum, renamed
`complete→done`, and switched `canceled→cancelled` (double-L) to match
the UK spelling used elsewhere.

Additional type-specific states live in separate fields, not in `status`:
- `group_state` on releases: `unreleased` / `released`
- `sprint_state` on sprints: `planned` / `active` / `complete`

---

## Project Fields (additions)

| Field | Type | Notes |
|-------|------|-------|
| `status` | TEXT | Project lifecycle: `active`, `frozen`, `archived`, or `deleted`. Only `active` projects accept new issues; M141 adds storage-level creation guards for every non-active state. |
| `product_owner` | INTEGER NULL | FK→users — project lead |
| `customer_label` | TEXT | Legacy external/customer label retained after M70 |
| `customer_id` | INTEGER NULL | FK→customers.id — assigned customer record |

---

## New Table: `time_entries`

Ticket-level time tracking. Per user, start/stop based.

```sql
time_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    started_at  TEXT NOT NULL,
    stopped_at  TEXT NULL,           -- NULL = timer currently running
    override    REAL NULL,           -- manual override in hours
    comment     TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
)
```

- Only tracks time on issues of type ticket/task (not groups)
- A running timer has `stopped_at = NULL`
- `override` allows manual correction without deleting the entry
- `internal_rate_hourly` (REAL, nullable) added in a later migration for per-entry internal rate snapshots
- **Shipped in v1.0.0.** API surface under `/api/time-entries` and `/api/issues/{id}/time-entries`

---

## Frontend View Model

Same tickets, multiple views:

```
Project Detail View
  ├── [Tab: Epics]       → tickets grouped by epic relations
  ├── [Tab: Cost Units]  → tickets grouped by cost_unit relations
  ├── [Tab: Releases]    → tickets grouped by release relations
  ├── [Tab: Sprints]     → tickets grouped by sprint relations (deferred)
  └── [Tab: All Tickets] → flat/filtered list (existing behavior)
```

Each tab shows the same ticket pool from a different organizational perspective. A ticket with no group relations appears as "ungrouped" in each view.

---

## Migration Strategy (high-level)

1. **Before anything**: create a tagged backup on live (`pre-v2-migration`)
2. Add new nullable columns to `issues` (group-level fields, sprint fields) — additive, safe
3. Create `issue_relations` table — new table, safe
4. Migrate existing `parent_id` epic→ticket relationships into `issue_relations` (type=groups) — data migration
5. Migrate existing free-text `cost_unit` values: create group entities of type `cost_unit`, then insert relations — data migration
6. Migrate existing free-text `release` values: create group entities of type `release`, then insert relations — data migration
7. Migrate existing free-text `depends_on`/`impacts` values: parse issue keys, resolve IDs, insert relations — data migration
8. Add `product_owner` (FK→users) and `customer_id` to `projects` — additive, safe
9. Align status values: `UPDATE issues SET status='backlog' WHERE status='open'` etc. — data migration
10. Deprecate old free-text columns (leave in DB, stop using in code) — or drop later
11. Create `time_entries` table — new table, safe, deferred

**Critical**: steps 4–7 and 9 are data migrations that transform existing live data. Each should be tested locally against a copy of the live DB before deploying.

---

## Implementation Priority

| Phase | What | Risk |
|-------|------|------|
| 1 | `issue_relations` table + group-level columns + status rename | Medium — data migration of existing relationships |
| 2 | Frontend views: epic/cost_unit/release tabs | Low — additive UI |
| 3 | Sprint type + sprint view | Low — additive |
| 4 | `time_entries` table + tracking UI | Low — new table, no migration |

---

## Open Questions (remaining)

- Search index — do group-level fields (budget, rates) need to be FTS-searchable? **Deferred.**
- Sprint Jira fields — **resolved**: keep both `jira_id` (numeric Jira ID) and `jira_text` (Jira text key) as separate columns on sprint issues. Reason: both may be needed during import for reliable mapping.

---

## Permission Model (v1.1.1)

PAIMOS uses a **two-layer** permission model:

1. **Role** (on `users.role`) — `admin` / `member` / `external`.
2. **Per-project access level** (on `project_members.access_level`) —
   `none` / `viewer` / `editor`.

| Level    | Read | Write | Notes                                               |
| -------- | ---- | ----- | --------------------------------------------------- |
| `none`   | no   | no    | Explicit denial; overrides the member default.      |
| `viewer` | yes  | no    | Read-only access to the project and its issues.    |
| `editor` | yes  | yes   | Full read + write within the project.              |

**Role defaults** when no `project_members` row exists:
- **admin** — always bypasses per-project checks (effectively editor everywhere).
- **member** — default `editor` on every non-deleted project.
- **external** — default `none`; must be granted explicitly.

**Auto-seeding:**
- `CreateUser` (admin/member) seeds `editor` rows for every non-deleted project.
- `CreateProject` seeds `editor` rows for every active admin/member.
- Migration 64 backfilled existing portal grants as `viewer` and seeded
  admin/member editors on pre-existing projects.

**Access audit** (`access_audit` table) logs grant / update / revoke
events with actor, old level, new level, and timestamp. Admin-only
read via `GET /api/access-audit`.

**Backend enforcement** — see `backend/auth/middleware_project.go` and
`backend/auth/access.go`:
- `RequireProjectView` / `RequireProjectEdit`
- `RequireIssueAccess` / `RequireIssueEdit`
- `RequireAttachmentAccess` / `RequireAttachmentEdit`
- `RequireTimeEntryAccess` / `RequireTimeEntryEdit`
- `RequireCommentAccess` / `RequireCommentEdit`
- Admin-only routes (project CRUD, user CRUD, etc.) use `auth.RequireAdmin`.

Response convention: **404** on no-view access (no existence oracle),
**403** on view-only-when-edit-required.

**Frontend:** `/auth/login`, `/auth/me`, `/auth/totp/verify` return
`{ user, access }`. The Pinia store exposes `canView(pid)` / `canEdit(pid)`
plus a hydrated `accessibleProjects` map. Router per-project guarding
via `meta.projectIdParam`.

See `docs/DEVELOPER_GUIDE.md` section 4a for the implementation walkthrough.

---

## Session & auth columns (v2.7.1+)

Four columns landed in the `v2.7.x` window. None are env-configurable;
all are operator-visible via the API or admin UI.

| Migration | Column                          | Ticket   | Purpose                                                                                                                |
| --------- | ------------------------------- | -------- | ---------------------------------------------------------------------------------------------------------------------- |
| M89       | `sessions.created_at`           | PAI-322  | Anchors the 90-day absolute lifetime cap independent of the sliding `expires_at`.                                       |
| M90       | `users.permissions_epoch`       | PAI-320  | Counter bumped on role / membership / status change. Surfaced as `X-Permissions-Epoch`; mismatch invalidates sessions. |
| M91       | `users.must_change_password`    | PAI-321  | Force-password-change gate. Default `1` for new users; cleared on first successful `POST /auth/password`.              |
| M92       | `users.is_super_admin`          | PAI-335  | Compatibility boolean for legacy super-admin reads.                                                                    |
| M105      | `users.role_key`                | PAI-336  | Canonical public role enum: `admin`, `member`, `external`, `super_admin`; writes mirror into legacy `role`/flag.       |
| M106      | `sessions.actor_user_id`        | PAI-389  | Real operator while a super-admin impersonation session is active.                                                     |
| M106      | `sessions.acting_as_user_id`    | PAI-389  | Effective user while a super-admin impersonation session is active.                                                    |

`sessions` is touched on every authenticated write — keep changes
additive. `users.role` keeps the older SQLite CHECK constraint as a
compatibility shim; application code reads `users.role_key`.

PAI-336 also adds `role_permissions` for seeded role capability checks
and `super_admin_audit` for queryable privileged-action traceability.
PAI-389 extends that audit feed with impersonation start/end rows and
mutating-request rows while the impersonation frame is active.

---

## Knowledge plane + agent attribution (v2.8.x — M93..M101)

The v2.8.x release cycle introduced the knowledge plane (project agents,
five new knowledge issue types, per-(user, device, project) sync watches)
and the agent-attribution split (who/which-session caused each mutation).
All migrations are additive — no data backfill, existing rows stay NULL
or default-valued.

### Knowledge plane on `issues` (M96 — PAI-338)

`issues` gained two columns plus extended CHECK constraints to host
knowledge entries as first-class rows:

| Column | Type | Notes |
|---|---|---|
| `slug` | TEXT NULL | Knowledge-type lookup key. Pattern `[a-z][a-z0-9_-]*`, max 64 chars, application-enforced. NULL on non-knowledge issues. |
| `category_metadata` | TEXT NULL | Per-type tail fields (e.g. `external_system.url`) as JSON-as-text. |

Plus the `type` CHECK now includes `memory`, `runbook`, `external_system`,
`related_project`, `guideline`; the `status` CHECK adds `archived` and
`proposed`. M96 initially added one nullable project index. M162 replaces it
with partial unique indexes for each live project, user, and instance scope;
non-knowledge and soft-deleted rows stay unconstrained.

### Cross-scope memory + reference tracking (M99 — PAI-345, M100 — PAI-347)

| Migration | Column | Type | Purpose |
|---|---|---|---|
| M99 | `issues.user_id` | INTEGER NULL REFERENCES users(id) | Discriminator for the three memory scopes: `(project_id NOT NULL, user_id NULL)` = project memory; `(project_id NULL, user_id NOT NULL)` = user memory; both NULL = instance memory (admin-only). M162 enforces exclusive ownership and user-owned memory types with database triggers. |
| M100 | `issues.reference_count` | INTEGER NOT NULL DEFAULT 0 | Increments on each `paimos session start --bundle full` resolve (PAI-340) and on auto-suggest surface (PAI-342). |
| M100 | `issues.last_referenced_at` | TEXT NULL | Wall-clock of the most recent reference. Pre-M100 rows treated as "freshly referenced" by the stale-proposal logic so the migration day doesn't flood the archive queue. |

Index: `idx_issues_user_type` partial — only rows with `user_id IS NOT NULL`.
M162 adds live-identity indexes on `(project_id,type,slug)`,
`(user_id,type,slug)`, and `(type,slug)` for the three respective scopes.

### Project agents + inventories (M94 — PAI-326, M95 — PAI-329)

The "what agents work this project" definition lives in project metadata
instead of per-repo local files. Three new tables — one for the declarable
agents themselves, two for inventories the agent artifacts inherit from.

#### `project_agents` (M94 + M95 extensions)

```sql
project_agents (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id           INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    slash_command_name   TEXT NOT NULL DEFAULT '',
    lane_tags            TEXT NOT NULL DEFAULT '[]',   -- JSON array
    metadata             TEXT NOT NULL DEFAULT '{}',   -- JSON object
    body                 TEXT NOT NULL DEFAULT '',     -- M95: markdown freetext (rendered skill body)
    bootstrap_steps      TEXT NOT NULL DEFAULT '[]',   -- M95: JSON array of {title, command, rationale}
    non_negotiable_rules TEXT NOT NULL DEFAULT '[]',   -- M95: JSON array of {title, body, memory_ref}
    created_at           TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
)
```

- UNIQUE INDEX on `(project_id, name)` — one agent name per project.
- `memory_ref` inside `non_negotiable_rules` is just a string here; resolution into an actual memory entry happens at render time (PAI-330).

#### `project_environments` + `project_deploy_recipes` (M95)

Project-level shared inventories the agent artifacts can reference by
name. Same shape — separate tables (mirrors the M75 `project_repos`
precedent: one row per item, no JSON-blob editing dance).

| Table | Key columns | Purpose |
|---|---|---|
| `project_environments` | `name`, `url`, `host_alias`, `host_ip`, `sort_order` | Staging vs prod, named hosts the agent body can address by alias |
| `project_deploy_recipes` | `name`, `command`, `summary`, `sort_order` | Named deployment shorthand the agent body can reference by name |

Both: UNIQUE on `(project_id, name)`; ordering index on `(project_id, sort_order, id)`.

`project_repos` (existing, M75) is the third leg of project-level
inventory and is reused as-is; the canonical agent-artifact endpoint
inlines all three.

### Durable agent message ledger and bus (M151–M173 — PAI-817, PAI-815, PAI-816, PAI-826, PAI-827, PAI-828, PAI-829, PAI-905)

`agent_messages` stores the M151 security fields (`from_agent_id`,
`to_agent_id`, optional `issue_id`, parent, hop, body, held/delivered state)
and the M152 public envelope: `message_id`, project `context_id`, optional
issue-key `task_id`, `role`, JSON `parts`/`metadata`, canonical `from_address`
and `to_address`, `reply_to`, `thread_id`, and the trusted `session_id`.
The numeric row ID is the monotonic listen cursor but is exposed only as
`cursor`; numeric agent IDs never enter the public write contract. The public
write accepts an optional `is_action_request` marker; marked rows are forced to
held/non-delivered state and remain visible only through human inspection.

M152 backfills M151 rows with stable `legacy-<id>` message/thread identifiers
and name-based addresses, then pins unique message IDs and addressee/thread
indexes. `agent_message_allowlist` remains the active sender gate. The M151
`agent_message_rate_limits` table is retained for migration compatibility;
the canonical M152 write path enforces its project-wide per-sender minute
budget from durable ledger rows. Issue-anchored rows are displayed in their own
agent message region, never inserted into or rendered as human comments.

M153 adds nullable `agent_messages.read_at` plus
`agent_message_cursors(project_id, project_agent_id, address, cursor,
updated_at)`. One row per project/address records the last acknowledged
delivered message. Acknowledgements are monotonic, must name a real delivered
non-action row in the attributed inbox, and mark only covered rows read; a
caller cannot acknowledge an arbitrary future cursor.

M173 adds immutable `agent_messages.expects_reply` with a default of false,
an authoritative one-row-per-message `agent_reply_obligations` projection,
and append-only `agent_reply_obligation_events`. Creation shares the message
transaction. Only an accepted exact counterpart `reply_to` transition closes
the obligation; held replies, delivery and acknowledgement do not. Due
generations become eligible for an authorized attention/listen projection poll;
there is no autonomous wall-clock scheduler. Polls append at most six
`reply_overdue` attention items (5 minutes,
15 minutes, 1 hour, 4 hours, 12 hours, and 24 hours); the open current-state
row then remains quiet with `resurface_count=6`. Closed obligations
remain in history but are filtered from the actionable view, and an otherwise
empty open batch becomes terminal `superseded` rather than falsely
`handed_off`.

M173 also adds append-only `agent_message_human_resolutions` for human
`resolved|dismissed` outcomes on held action requests. The record stores only
value-free current user/session attribution and digested idempotency/request
material. The original held message is never released or rewritten. Session
home and the PAI-902 attention projection anti-join the resolution so current
state changes immediately while immutable message/attention history remains.

M154 adds immutable `delivery_level` (`simple|steer`), fixed
`delivery_fallback='simple'`, and nullable primary/fallback target-version IDs
to every canonical envelope. Existing rows backfill to `simple` with no target.
`agent_message_idempotency` keeps a scoped SHA-256 key digest, normalized
request digest, and canonical message row for the life of that message; exact
replays return the original row and conflicts fail without creating another
message or delivery.

`agent_message_targets` stores versioned, per-instance, project/address-owned
bindings. Only one primary and one simple fallback version may be enabled for
an address. Vendor references are domain-separated secretvault ciphertext;
ordinary ledger, target-list, delivery-status, and webhook payloads contain
only non-secret target IDs and kinds. The shipped adapters in M154 are `codex`
with `codex_thread` and `grok_bot_routine` with `https_webhook`. M155
(PAI-827) rebuilds the table in place, keeping every version, ciphertext, and
delivery reference, to add `claude_resume` and `claude_channel` with
`claude_session` (an encrypted local session UUID or `session_…`/`cse_…`
cloud id). M156 rebuilds the table once more so `adapter` and `target_kind`
are lowercase plugin keys rather than vendor allowlists. Adapter/kind pairing
and maximum-level capability remain fail-closed in the Go harness registry;
unknown adapters are unsupported. M157 (PAI-828) adds the nullable
`target_secret_cipher` column: the receiver-owned sender secret a server-side
webhook vendor requires in one request header (Grok Bot routines:
`Authorization: Bearer <sender key>`), encrypted under the separate
`agent-message-target-secrets` secretvault domain. Adapters without that
capability keep `NULL`, read surfaces expose only `has_secret`, listen never
discloses it, and `paimos secrets rotate` re-encrypts both columns together.

`agent_message_deliveries` is one unique intent/outbox row per eligible
message, with stable `delivery_id`, snapshotted targets, requested/effective
level, typed fallback reason, state (`pending`, `leased`, `retry`, `blocked`,
`handed_off`, `dead`), attempts, lease/retry timestamps, redacted error code,
and handoff timestamp. It stores no second message body. Held and action rows
have no runnable delivery; missing-target rows remain explicitly blocked until
an operator attaches current targets and requeues them.

### Auto-watch sync subscriptions (M98 — PAI-331)

Per-(user, device, project) opt-in for the sync engine's SSE push
channel. Default OFF — a fresh (device, project) tuple does not
auto-receive updates. PAI-341 (knowledge-plane sync) reuses this table
verbatim; one row covers all kinds for that triple.

```sql
auto_watch_subscriptions (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id   TEXT NOT NULL,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    enabled     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, device_id, project_id)
)
```

Toggling `enabled` OFF invalidates the device's active SSE connection
server-side. Subscriptions are managed under
**Settings → Account → Auto-watch sync**.

### Agent/session attribution (M93 — PAI-324, M101 — PAI-354)

Two parallel nullable-column adds to capture who/which-session caused a
mutation, without forcing a backfill:

| Migration | Table | Column |
|---|---|---|
| M93 | `issue_history` | `agent_name TEXT NULL`, `session_id TEXT NULL` |
| M101 | `mutation_log` | `agent_name TEXT NULL` (session_id already arrived via M83) |

Write endpoints persist the values from the `X-Paimos-Agent-Name` and
`X-Paimos-Session-Id` headers when present; existing rows pre-M93/M101
stay NULL. Length cap is enforced application-side (64 chars each,
`handlers.agentAttrCap`) before the INSERT — SQLite `ALTER TABLE` can't
add CHECK retroactively. PAI-209 undo/redo is unaffected; the new
columns are purely informational.

### Report, portal, comment visibility, idempotency, issue counters (M107-M114)

The post-M101 migration ledger is active in `backend/db/db.go` and should stay reflected here:

| Migration | Area | Purpose |
|---|---|---|
| M107 | `project_cooperation`, `project_report_permissions`, `project_report_snapshots` | Projektbericht report metadata, immutable snapshot evidence, report-facing permissions. |
| M108 | `issues.report_summary` | Customer-facing report text used by Projektbericht export and portal acceptance pages. |
| M109 | `CUSTOMERPORTAL` system tag | The marker that makes issues visible in the customer portal. |
| M110 | customer-portal backfill | One-time terminal-status visibility backfill plus `mutation_log` audit rows with `undoable=0`. |
| M111 | `comments.visibility` | Internal vs external comment visibility; new comments default to `internal`. |
| M112 | `idempotency_keys` | Duplicate-prone create-write idempotency cache scoped by key, user, route, and method. |
| M113 | `project_issue_counters`, `idx_issues_project_number_unique` | Atomic per-project issue-number allocation plus a database uniqueness backstop (PAI-554). |
| M114 | `customers.tax_id`, `customers.company_register_number` | Explicit customer legal identifiers: UID/tax number and Firmenbuchnummer/FN (PAI-558). |
| M115 | `issues.content_rev`, list-freshness triggers | Derived issue-list content marker for time entries, tags, relations, and tag metadata. |
| M116 | `time_entries.material_lp` | Optional per-entry material LP for time-and-material reporting. |
| M117 | `entity_embeddings.provider/status/error` | Retrieval metadata for provider and degraded indexing state. |
| M118 | `issue_relations.type='parent'` | Backfill and mirror hierarchy into parent edges while keeping legacy column compatibility. |
| M119 | `idx_issue_relations_one_parent` | Database invariant: at most one parent edge per child. |
| M120 | `issues.parent_id` drop | Parent relation edge becomes the hierarchy source of truth. |
| M121 | `issue_relations.type='cost_unit'/'release'` | Typed relation edges for cost-unit and release containers. |
| M122 | cost-unit/release backfill + unique indexes | Create missing containers, edge labelled issues, and enforce one edge per ticket. |
| M123 | `issues.cost_unit`, `issues.release` drop | Cost-unit/release relation edges become the source of truth. |
| M124 | `issues.content_revised_at`, `issues.deps_reviewed_at` | Knowledge dependency review timestamps for computed needs-re-review state. |
| M125 | `agent_runs` | Implement-this run lifecycle records. |
| M126 | `auto_watch_subscriptions.can_implement` | Runner capability advertisement for implement-capable subscribers. |
| M127 | `idx_agent_runs_active_issue` | Database invariant: at most one active implement run per issue. |
| M128 | `agent_runs.claimed_by` | Claim ownership for queued-to-running implement runs. |
| M129 | `agent_runs` provider/action fields, `auto_watch_subscriptions.actions_json` | Explicit implement-this provider/action metadata and runner capability payloads. |
| M130 | `ai_calls.profile_id/effort/prompt_preset_ref/context_pack` | Safe AI execution-option provenance for action calls. |
| M131 | `ai_settings.base_url`, `agent_runs` draft metadata/status | OpenRouter/local-model draft provider support, including `drafted` status and safe run provenance. |
| M132 | `agent_runs.source_draft_run_id/followup_run_id`, `projects.ai_defaults_json/ai_policy_json` | Draft→follow-up handoff links and per-project AI defaults/policy metadata (PAI-665/PAI-666). |
| M133 | `issue_key_aliases` | Former issue keys keep resolving after cross-project moves (PAI-690). |
| M134 | `intake_sessions`, `intake_events` | Voice-intake workbench sessions: append-only per-session event log doubling as time-travel history and SSE replay source (PAI-704). |
| M135 | `users.intake_confidence_threshold` | Per-user override for the voice-intake auto-switch confidence threshold; NULL = instance default (PAI-706). |
| M136 | `ai_settings.voice_*` | Speech-to-text provider settings (ElevenLabs) for voice intake: provider, encrypted key, base URL (EU residency host support), model (PAI-710). |
| M137 | `ai_settings.tts_voice_id/tts_model` | Text-to-speech settings for the intake understanding check (PAI-714). |
| M138 | seeded admin promotion | Promote the seeded `admin` only when no super-admin exists, restoring a path to privileged administration (PAI-739). |
| M139 | `sessions.via_oidc` | Record OIDC-authenticated sessions so local-TOTP guidance does not misrepresent the IdP boundary (PAI-742). |
| M140 | `agent_runs.repo_url/branch_name/commit_base_sha/commit_sha` | Runner-declared base→head Git evidence; equal SHAs explicitly mean no commit was produced (PAI-702). |
| M141 | project lifecycle issue-insert triggers | Reject issue creation in `frozen`, `archived`, or `deleted` projects at the storage boundary (PAI-754). |
| M142 | `agent_run_telemetry`, `agent_run_telemetry_latest` | Append-only provider-neutral run facts plus an indexed latest projection (PAI-799). |
| M143 | rebuilt `agent_runs`; expanded telemetry latest projection | Add truthful terminal `completed`, durable `expects_supervisor_telemetry`, and separate latest event/heartbeat/semantic/estimate pointers (PAI-801). |
| M144 | `deliveries` and immutable delivery fact tables; `delivery_change_log`; `agent_runs.delivery_instrumentation_version` | Issue-rooted end-to-end delivery attempts, stage lineage/evidence, duration history, and deletion-safe invalidation identity (PAI-802). |
| M145 | Agent Mode change audiences, legacy roots, and privacy guards | Revoked-project replay, live-only version-0 synthetic provenance, recursive metadata invalidation, and upgraded secret-like text backstops (PAI-804). |
| M146 | `comments.client_request_id`, `idx_comments_author_client_request` | Per-author exact-once identity for confirmed internal notes. The partial unique index is the atomic backstop; a CHECK requires an authenticated author, internal visibility, ASCII-safe syntax, and a 128-byte maximum (PAI-808). |
| M147 | supervisory control grants, approvals, commands, runtime state, outbox, and append-only control events | Principal-bound, compare-and-swap Agent Mode controls with exact target snapshots and auditable execution (PAI-809). |
| M148 | external-stage registrations, handoffs, credentials, owner/dependency streams, projections, setup/audit events | Frozen v1 Pharos owner and Janus dependency reporting with exact authority, replay, and secret boundaries (PAI-810). |
| M149 | `external_stage_owner_activation_events` | Principal-attributed, append-only proof for the additive internal Pharos owner-activation route; released M148 remains immutable (PAI-810). |
| M150 | `agent_runs.implementation_result_digest` | Optional immutable source-free SHA-256 binding for the exact bounded covered repository source surface observed stable around successful tests when no commit exists; ignored payloads and the external execution environment are outside the binding (PAI-810). |
| M151 | `agent_messages`, `agent_message_allowlist`, `agent_message_rate_limits` | Untrusted-message security contract: per-receiver sender allowlists, hop/rate/size bounds, and held action-request rows that are never delivered as executable (PAI-817). |
| M152 | `agent_messages.message_id/context_id/task_id/role/parts_json/…`, unique message-ID and addressee/thread indexes | Canonical project-scoped A2A envelope with name-based addresses; M151 rows are backfilled to `legacy-<id>` identifiers (PAI-815). |
| M153 | `agent_messages.read_at`, `agent_message_cursors` | Durable, monotonic per-project/address receiver cursors bound to attributed acknowledgements (PAI-816). |
| M154 | `agent_messages.delivery_level/delivery_fallback/delivery_*_target_id`, `agent_message_targets`, `agent_message_deliveries`, `agent_message_idempotency` | Message-level `simple`/`steer` intent, encrypted versioned receiver targets (`codex`, `grok_bot_routine`), one linked delivery/outbox row per eligible message, and atomic send idempotency (PAI-826). |
| M155 | rebuilt `agent_message_targets` | Adds the `claude_resume` and `claude_channel` adapters with `claude_session` targets while carrying every existing version and ciphertext over; Claude targets are fixed to `maximum_level='simple'` (PAI-827). |
| M156 | rebuilt `agent_message_targets` | Replaces schema-level vendor allowlists and pairings with bounded lowercase harness plugin keys while preserving all target rows, ciphertext, enabled state, indexes, and foreign-key references; plugin binding and capability validation remain in Go (PAI-829). |
| M157 | `agent_message_targets.target_secret_cipher` | Nullable, domain-separated ciphertext for the receiver-owned sender secret a server-side webhook adapter sends as `Authorization: Bearer` (Grok Bot routine sender key); existing rows keep `NULL`, and a version without it is never dispatched (PAI-828). |
| M173 | `agent_messages.expects_reply`, `agent_reply_obligations`, `agent_reply_obligation_events`, `agent_message_human_resolutions`; rebuilt attention items/batches | Opt-in exact-reply closure, bounded overdue resurfacing, value-free idempotent human disposition of held action requests, and immutable-history/current-state separation (PAI-905). |

`agent_runs.status=completed` means implementation finished without a configured
test command; it never implies tests passed. `tests_passed` and `tests_failed`
require a non-empty `tests_summary` of at most 4096 bytes, supplied by the same
transition or already persisted. For a current version-1 instrumented
implementation run whose attempt requires QA, `tests_passed` additionally
requires an allowlisted implementation commit, source-free implementation
digest, or same-issue attachment and
atomically records eligible implementation success plus eligible QA success;
legacy version-0 runs are not backfilled. The QA `test_result` digest binds the
run id, exactly one selected implementation reference (commit, else digest,
else attachment), and the exact persisted bytes of the test summary. Unselected
lower-priority transport fields do not enter that canonical tuple. A
terminal QA execution is retried with exact current execution/epoch CAS, while
an active human or external QA execution is never replaced. Later `deployed` or
`failed` run statuses do not erase immutable valid test evidence; new upstream
lineage can make it ineligible. Telemetry history is append-only. Its latest
projection keeps independent event, heartbeat, semantic, and estimate pointers;
only the server-received heartbeat timestamp is liveness evidence. M143
reconstructs the complete projection from append-only history, including
missing or stale pointer rows. Telemetry
`activity` and `estimate_basis` are valid single-line UTF-8 bounded to 280 and
240 bytes respectively; adapters truncate on code-point boundaries to those
same byte limits, CLI/MCP reject oversized values locally, and SQLite measures
`CAST(... AS BLOB)` so storage cannot reinterpret bytes as code points. Exact
persisted sequence/body replays remain idempotent after
any result status, while conflicting or new facts are rejected. Telemetry treats
`tests_passed` and `tests_failed` as stream-closing results even though the run
lifecycle still permits the explicit deploy/fail transitions that follow them.

### Delivery supervision model (M144 — PAI-802)

A delivery has the stable identity `issue:<issue_id>`. Existing issues are not
backfilled: a read with no `deliveries` row returns an uninstrumented `unknown`
projection, augmented only by active version-0 legacy runs. The first explicit
post-M144 write creates the container. Every new run creation path stamps
`agent_runs.delivery_instrumentation_version=1` and creates its immutable
`delivery_agent_run_links` row in the same transaction.

Each authoritative `delivery_attempts` row is an immutable, atomically sealed
plan revision with exactly five policy facts in canonical order: specification,
implementation, QA, deployment, verification. Default weights are
10/45/20/15/10. A project-policy
change starts another attempt; a routine retry starts another stage execution
inside the same attempt. Execution starts record the exact eligible predecessor
event, so retrying an upstream stage makes old downstream evidence ineligible
without deleting it. Handoffs advance authority epochs and late reports from an
old attempt, execution, reporter, or epoch fail closed.

`delivery_events` owns reporter-and-kind-scoped bounded idempotency keys,
canonical payload hashes, and contiguous per-delivery revisions. Typed
`delivery_stage_events`, blockers, and evidence are append-only;
`delivery_stage_latest` contains rebuildable pointers
for authority, semantic state, heartbeat, and estimate separately. Required
stage success needs a current succeeded semantic report, no current blocker,
passed allowlisted evidence, and current prerequisite lineage. Deployment
success remains independently visible through the `Deployed` and `Unverified`
axes until required verification succeeds. While verification is pending the
display state is `deployed_unverified`; a current verification failure or
cancellation takes display/suppression precedence as `failed_needs_retry` or
`cancelled` without erasing either independent deployment axis.
The internal transaction-aware bulk reducer reads 1–1,000 already-authorized
roots in one fixed SQL round trip and captures one calculation timestamp.
When a linked run closes after its attempt, execution, or authority was
superseded, M144 appends one idempotent `run_lifecycle_observed` envelope. That
event advances the delivery revision/change stream and appears by identity in
bounded attempt history, but cannot reopen or rewrite current stage truth.

One immutable `delivery_stage_durations` sample is written for each eligible
successful execution, bound to its exact execution-start and terminal-success
facts, current prerequisite lineage, completion project, and completion time.
Server RFC3339 timestamps are parsed, intervals are
unioned and clipped, human wait takes precedence over other blocking, and the
remaining lead time is active. The samples retain project-at-completion and are
never removed by a later retry.

`delivery_change_log` has no root foreign key. It retains a safe removal fact
when a visible issue is deleted and gives each delivery a contiguous change sequence;
the opaque global id is internal only. Rows cannot be updated or directly
deleted. An append-only monotone retention ledger is the sole prefix-deletion
path, and its guards fail closed if retention state is missing or malformed.
The encompassing SQL transaction writes facts and the durable change row;
observer callbacks are dispatched only after commit. Agent Mode seals its
authorized cursors around this internal high-water rather than expose database
ids or tokens.

### Agent Mode invalidation provenance (M145 — PAI-804)

M145 extends `delivery_change_log` with a nullable `revoked_project_id` and adds
one-shot pending move provenance to canonical and legacy roots. A visible
project move writes the new current project and the exact old project in one
transaction. The target audience receives a refetch; a source-only audience
receives an identity-free reset. Direct inserts cannot forge a different old
project, reuse move provenance, skip a per-root sequence, or append facts for a
hidden root. The effective current high-water is `max(log tail, retention
floor)`, so pruning the complete retained prefix cannot make a fresh cursor
start below the floor.

`agent_mode_legacy_roots` is bounded provenance for active instrumentation-v0
runs that have no canonical `deliveries` row. Its negative synthetic ID and
`issue:<id>` key are database-derived and immutable; caller-supplied identities
are rejected. A root is visible only while its issue is live, at least one v0
run is active, and no canonical delivery exists. Retained terminal provenance
is not candidate authority and does not participate in later metadata fanout.
The table deliberately has no cascading issue foreign key: visible issue/run
removals append before leaving membership, then cleanup can retain or remove
provenance without inventing a tombstone audience.

Each committed issue, tag, lane, recursive-parent, or project trigger appends
exactly one durable invalidation per affected currently visible canonical or
legacy root. A reparent implemented as a delete plus insert can therefore
append consecutive lane facts; replay coalesces them to the latest authorized
hint for that root.
Hidden moves still synchronize project hints for a later restore but emit no
observable change; restore emits one current-project refetch. Run lifecycle
updates, delivery facts, and their change rows share the same transaction.
Process-local wakeups run only after commit and are an optimization over the
durable log and polling, so rollback, restart, or a lost/coalesced wake cannot
publish uncommitted truth or strand a stream.

### Exact-once internal comments (M146 — PAI-808)

`comments.client_request_id` is nullable, so ordinary and pre-M146 comments
retain their previous behavior. When present, it is unique with `author_id`
and structurally restricted to internal comments. Creation inserts first and
uses the unique collision as the only replay decision: the handler returns the
original row only when issue, body, author, and internal visibility match;
otherwise it returns `409`. Mutation history is emitted only by the transaction
that inserted the row, and undo/redo preserves the identity. Keyed comments
cannot be flipped external. They bypass `idempotency_keys`, preventing a second
stored copy of a confirmed private note response.

PAI-553 tracks the remaining hardening: keep this ledger and the published schema version aligned whenever future migrations land.

---

## Related

- Legacy v0.3.5 schema snapshot: `docs/archive/DATA_MODEL.md`
- Implementation guide: `docs/DEVELOPER_GUIDE.md`
