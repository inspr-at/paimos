# Planning hierarchy

Aeon keeps one work tree. A project is not a separate table. It is a node whose kind slug is `project`, and it follows the same parent, relation, search, history, and move rules as every other node.

## Nodes and kinds

A node has a tenant, an immutable key, a kind, a title, a body, a JSON fields object, a state, a parent, and a decimal position among its siblings. Delete is soft. The key looks like `PRJ-4` or `LUMEN-1`: a short prefix, a hyphen, and a positive integer. The tenant allocates the next number in the same transaction as the insert. An explicit key is kept and advances that prefix's counter.

Kinds belong to the tenant. The slug is immutable because parent rules name it. The label, short prefix, icon, allowed child slugs, and JSON Schema for `fields` are configuration. A null child list permits any child. An empty list permits none. Narrowing the list affects new children only. Existing children stay movable and deletable.

New tenants start with project, work, release, memory, runbook, and guideline.
Nesting and leaf shape decide whether work is presented as an epic, ticket or
task; those names no longer require separate kinds. Later modules add kinds such
as work_order, requirement, and the business kinds (cost_unit, organisation,
contact, quote) when those features are configured. `external_system` and
`related_project` are created on first use.

State is a non-empty string chosen by the tenant. The product screens treat `new` and `backlog` as open, `in_progress` as doing, `done` as finished, and `cancelled` as dropped. Knowledge uses its own trio: active is stored as `backlog`, proposed as `proposed`, and archived as `cancelled`.

## Relations

Relations are directed links between two live nodes: `blocks`, `relates`, `implements`, `cites`, and `duplicates`. `relates` is symmetric and stored with the smaller id as the source. CRM adds `customer_of` and `contact_for`, checked for kind and direction. A relation write needs permission on both ends. The change is one tenant event.

## Projects

`GET /api/projects` lists project nodes. The route key people use in `/p/:projectKey` is `fields.project_key` when set, otherwise the classic key, otherwise the prefix of the node key. The node key itself never changes. Moving a node into another project is a separate project-move, not an edit of the key.

## Releases

A release is a node of kind `release`. Existing legacy-named release tables
retain sequence, state, membership and published history during retirement.
`POST /api/projects/{id}/releases` atomically opens a planning release and adds
existing work, with a bounded ticket set and an idempotency key. It requires a
person with release and node write permissions. No stage, gate or handoff runs.
The release walker and membership APIs continue to read and edit planning work.
`GET /api/projects/{id}/releases` returns at most 100 records, newest first;
`truncated` signals another page, fetched with the last record's number as
`before_number`. The picker refuses to infer availability from a partial list.

## Retired Flow

The historical eight-stage INSPR Flow / Journey is retired by AEON-723,
shipped in [Direct Dome](https://github.com/inspr-at/paimos/releases/tag/v261007035250.0.0).
Its view, actions, gates,
handoffs and external-stage CLI are removed. Historical data stays in place;
compatibility HTTP routes return authenticated 410 errors. A later contract-phase
change will migrate shared release storage before removing dormant Flow data.

[INSPR Flow 2 (AEON-821) is planned](features/web-workspace.md#planned-work)
as a configurable workflow on the delivery engine: Idea → Requirements → Plan →
Build → Deploy → Access → Learn. It does not reuse the retired Journey code or
make those historical stages available again.
