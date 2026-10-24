<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Aeon architecture guide

Read this after [AGENTS.md](../AGENTS.md), before choosing an implementation shape. It maps the repository and gives copyable, deliberately small excerpts from current code. Each `path:line` link names the start of the relevant code; a fenced example's `source=` identifies the exact copied lines. Refresh both when changing an example. The source guard described below checks citations and excerpts; it does not prove architectural or security correctness.

The decisions and their rationale stay in PPM: [ADR-001 · foundation](https://aeon.barta.cm/p/AEON/knowledge/guideline/adr-001-foundation) and [ADR-002 · stack](https://aeon.barta.cm/p/AEON/knowledge/guideline/adr-002-stack). Only the working consequences are needed here: a tenant isolates data; configurable nodes, typed relations and a durable event log form the core; people and agents have explicit authority; one Go server and Postgres support a Vue client through OpenAPI. AGENTS.md supplies the later work-tree and retirement rules. The ADRs' original Journey and separate epic/ticket/task descriptions are historical, not instructions to restore those features.

## 1. Vocabulary → storage and packages

| Word | Meaning in code | Start here |
| --- | --- | --- |
| **Tenant / workspace** | Isolation boundary, not a project. `tenant_id` belongs in resource rows and composite references. The tenant row itself has `id`. A principal carries exactly one tenant into a transaction. | [internal/tenant/tenant.go:20](../internal/tenant/tenant.go#L20); [internal/db/db.go:67](../internal/db/db.go#L67) |
| **Principal** | A `person` or `agent` acting in a tenant. A person's OIDC identity and an agent's key are authentication mechanisms, not alternate domain actors. Current role bindings, project access and key ceilings determine permissions. | [internal/tenant/tenant.go:12](../internal/tenant/tenant.go#L12); [internal/authz/require.go:123](../internal/authz/require.go#L123) |
| **Node** | The shared tree record: kind, parent, ordered position, title, Markdown body, JSON fields, state and soft deletion. Projects, work, knowledge and releases are interpretations of nodes, not independent core trees. | [internal/db/migrations/0100_nodes.sql:107](../internal/db/migrations/0100_nodes.sql#L107); [internal/nodes/module.go:21](../internal/nodes/module.go#L21) |
| **Kind** | Tenant configuration with a stable slug, display label, prefix, icon, allowed child kinds and field schema. A work item's leaf shape and depth decide its presented name; create work rather than choosing an old epic/ticket/task kind. | [internal/nodes/kinds.go:17](../internal/nodes/kinds.go#L17); [internal/nodes/work_shape.go:12](../internal/nodes/work_shape.go#L12); [internal/nodes/work_shape.go:21](../internal/nodes/work_shape.go#L21) |
| **Label** | Human wording, not an identifier or permission. `node_kinds.label` and work vocabulary names/icons are configuration; state display wording has its own status-name configuration. User tags are a distinct nodes feature, not a new generic label table. | [internal/nodes/vocabulary.go:30](../internal/nodes/vocabulary.go#L30); [internal/nodes/kinds.go:17](../internal/nodes/kinds.go#L17); [internal/nodes/tags.go:17](../internal/nodes/tags.go#L17) |
| **Relation** | A typed edge between two nodes. `relates` canonicalizes endpoint order; directed kinds retain direction. Link mutations require authority in both endpoints' projects. | [internal/relations/module.go:29](../internal/relations/module.go#L29); [internal/relations/module.go:330](../internal/relations/module.go#L330) |
| **Event** | Durable tenant-local position, actor, optional node, type and snapshots. The resource tables store current state; event history powers audit, live updates and explicit compensating Undo. Do not assume every table is rebuilt by replay. | [internal/events/writer.go:19](../internal/events/writer.go#L19); [internal/db/migrations/0101_relations_events.sql:67](../internal/db/migrations/0101_relations_events.sql#L67) |
| **File / attachment** | Attachment metadata belongs to a node; immutable bytes and variants belong to a tenant and content hash. The current `attachments.Store` uses a tenant-namespaced filesystem, despite the ADR's S3 direction. | [internal/db/migrations/0540_attachments.sql:2](../internal/db/migrations/0540_attachments.sql#L2); [internal/attachments/storage.go:36](../internal/attachments/storage.go#L36); [internal/attachments/storage.go:84](../internal/attachments/storage.go#L84) |

State changes on work parents follow children; agents execute leaves. Use the work-shape and status derivation paths, not a script that sets each ancestor's status. Existing planning vocabulary and retired storage are explained in [planning hierarchy](PLANNING_HIERARCHY.md); current shape enrichment starts at [internal/nodes/work_shape.go:47](../internal/nodes/work_shape.go#L47) and transaction-end derivation at [internal/db/db.go:187](../internal/db/db.go#L187).

## 2. Package map and dependency direction

Verified against the production Go import graph at source baseline `26577bec96e0` with `go list -deps` (197 repository packages for the commands below, without tests or `webembed`). These commands inspect dependencies; they do not run a build or a test:

```sh
go list -deps ./cmd/... ./internal/...
go list -deps -f '{{.ImportPath}} {{join .Imports " "}}' ./cmd/... ./internal/...
```

The important existing direction is:

```text
cmd/aeon (composition) -> auth + feature modules + events + httpapi + db + web
feature modules       -> events -> authz -> httpapi -> db -> tenant
                                   |                  |
                                   +-> identity       +-> config, fieldschema,
                                                          linkvault, workerlease
cmd/aeon-agentd / internal/cli -> client + local runtime and harness packages
web/src -> HTTP contract and event streams (no Go imports)
```

This is a map of selected edges, not a claim that each package occupies one pure layer. For example `features` contains both `Service.Save` and HTTP handlers; `authz` imports HTTP response helpers; `db` has migration dependencies beyond `tenant`. Confirm the relevant direct imports before introducing a new dependency.

| Area | Responsibility and import boundary | Evidence |
| --- | --- | --- |
| `cmd/aeon` | Composition root: constructs services, mounts modules, supplies external clients, connects Undo handlers and starts workers. `cmd/aeon-agentd` is the separate local supervisor entry point. Commands may import internal packages; reusable packages must not import `cmd`. | [cmd/aeon/serve.go:496](../cmd/aeon/serve.go#L496); [cmd/aeon/serve.go:578](../cmd/aeon/serve.go#L578); [docs/AGENT_INTEGRATION.md:19](../docs/AGENT_INTEGRATION.md#L19) |
| `tenant`, small value packages | Request actor/context and shared values. Keep new value types independent of handlers and business modules. `tenant` has no repository imports. | [internal/tenant/tenant.go:7](../internal/tenant/tenant.go#L7) |
| `db`, `dbtest` | Pool, tenant transaction entry, fences, embedded schema, backstops and status derivation; test databases use a restricted app role. Production packages do not use `dbtest`. `db` must remain independent of `authz`/`events` to preserve the existing direction. | [internal/db/db.go:7](../internal/db/db.go#L7); [internal/dbtest/dbtest.go:40](../internal/dbtest/dbtest.go#L40) |
| `httpapi` | Shared HTTP module interface, server composition, budgets, body readers, responses and SPA routing. Modules import it; it does not import concrete feature handlers. Keep concrete registration in `cmd/aeon`. | [internal/httpapi/server.go:23](../internal/httpapi/server.go#L23); [internal/httpapi/server.go:109](../internal/httpapi/server.go#L109) |
| `auth`, `authz`, `identity` | `auth` authenticates cookies/keys/OIDC and performs route admission; `authz` loads current grants and enforces scopes; identity/link helpers resolve who an actor represents. Feature packages call `RequireTx`, not the auth HTTP module. | [internal/auth/module.go:980](../internal/auth/module.go#L980); [internal/authz/require.go:119](../internal/authz/require.go#L119) |
| `events` | Common append/history/stream/Undo mechanism. It imports `authz`, `db`, `httpapi`, `tenant` and privacy helpers, not concrete node/attachment/group handlers. Domains return Undo callbacks; the composition root registers them. | [internal/events/module.go:29](../internal/events/module.go#L29); [internal/events/module.go:39](../internal/events/module.go#L39) |
| Domain modules | `nodes`, `relations`, `knowledge`, `attachments`, `views`, `goals`, `inbox`, `agentruns`, `workorders`, `delivery`, routines and business packages own their behavior and typed SQL. Reuse established domain APIs; introduce a narrow callback/interface at a boundary when a reverse import would form a cycle. Do not turn `db` or `httpapi` into a place for a new feature's business logic. | [internal/features/features.go:56](../internal/features/features.go#L56); [internal/projectgroups/module.go:87](../internal/projectgroups/module.go#L87) |
| External and local-runtime boundaries | `openrouter`, `modelprovider`, `crossreview` and OIDC code translate outside protocols. Agentd/native/harness packages own local process and vendor interactions; an API handler's package is not authority to launch or approve a process. `client` is the shared HTTP client for CLI/MCP. | [internal/openrouter/client.go:43](../internal/openrouter/client.go#L43); [internal/client/client.go:80](../internal/client/client.go#L80); [internal/nativeintegration/root/authority.go:30](../internal/nativeintegration/root/authority.go#L30) |
| `plugins`, business plugins | Compiled, typed, first-party extensions with validated manifests and a sealed registry. Reuse registration and installation checks, rather than inventing a dynamic module loader. | [internal/plugins/registry.go:15](../internal/plugins/registry.go#L15); [cmd/aeon/serve.go:288](../cmd/aeon/serve.go#L288) |
| `web` | Vue/TypeScript browser application. Shared styles, stores, library clients and components live under `web/src`; route components compose these. The Go `web` package embeds built assets only with `webembed`. | [web/src/main.ts:4](../web/src/main.ts#L4); [web/embed.go:3](../web/embed.go#L3) |

When extending a package, rerun the two import commands for its command roots. Existing cross-domain imports are specific dependencies, not permission to create arbitrary peer imports. Keep a single acyclic direction and inject the reverse call from the composition root. One intentional cycle-avoidance exception is `authz`'s local event insertion helper; see §9.

## 3. Worked endpoint: feature override, contract → transaction → RLS → test

Use `PUT /api/settings/features/{key}` as the small complete example. It has a bounded request, strict decoding, a typed service input/result, optimistic revision checks, current person authorization under the tenant fence, tenant-scoped parameterized SQL and an atomic event. Feature availability itself grants no permission. See [internal/features/features.go:210](../internal/features/features.go#L210) and [internal/features/features.go:137](../internal/features/features.go#L137).

**Contract first.** Author the route in `api/areas/settings.yaml`; reuse `FeatureSetting`, declared there, and `FeatureEvaluation`, supplied by the area sources. `api/openapi.base.yaml` supplies common metadata/components. The following request shape is copied exactly; new descriptions must be quoted even where existing descriptions are not.

[api/areas/settings.yaml:193](../api/areas/settings.yaml#L193)

```yaml source=api/areas/settings.yaml:193
              type: object
              additionalProperties: false
              required: [enabled, expected_revision]
              properties:
                enabled: {type: [boolean, 'null']}
                expected_revision: {type: integer, minimum: 0}
```

The route, security and response are at [api/areas/settings.yaml:180](../api/areas/settings.yaml#L180); the result schema is at [api/areas/settings.yaml:561](../api/areas/settings.yaml#L561). Rebuild the generated document with `node api/generate.mjs --write` and check it with `go test ./internal/reportercontract/` in the assigned test lane. Never edit `api/openapi.yaml` directly. The generator is [api/generate.mjs:57](../api/generate.mjs#L57); schema pins and quoted-description checks are [internal/reportercontract/pins_test.go:56](../internal/reportercontract/pins_test.go#L56) and [internal/reportercontract/pins_test.go:227](../internal/reportercontract/pins_test.go#L227).

**Mount and admit.** `Service.Mount` registers exact stdlib mux patterns; the composition root includes `features.New(pool)`. The authz domain declares both permission mapping and which reads may use any-project grants with project RLS. Add new routes deliberately to those declarations and their strict fixtures. A pinned external response change also requires the appropriate Aeon-Contract version/pin update.

[internal/features/http.go:54](../internal/features/http.go#L54)

```go source=internal/features/http.go:54
func (s *Service) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/features", s.evaluateHTTP)
	mux.HandleFunc("GET /api/settings/features", s.settingsHTTP)
	mux.HandleFunc("PUT /api/settings/features/{key}", s.saveHTTP)
}
```

Follow [internal/authz/routes_features.go:6](../internal/authz/routes_features.go#L6) and [internal/authz/route_matrix_test.go:15](../internal/authz/route_matrix_test.go#L15), not an anonymous handler that bypasses route admission.

**Bound and decode before work.** Here the body is limited to 4096 bytes, unknown fields and extra JSON values are refused, and explicit `null` differs from omission. The service also validates known keys, project UUIDs and nonnegative revisions before entering the transaction.

[internal/features/http.go:103](../internal/features/http.go#L103)

```go source=internal/features/http.go:103
	dec := httpapi.NewRequestDecoder(w, r, 4096)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF || len(in.Enabled) == 0 || in.ExpectedRevision == nil {
		writeError(w, fault(400, "enabled and expected_revision are required"))
		return
	}
	var enabled *bool
	if err := json.Unmarshal(in.Enabled, &enabled); err != nil {
		writeError(w, fault(400, "enabled must be true, false or null"))
		return
	}
```

Shared request budgets and decode/deadline behavior live in [internal/httpapi/body.go:74](../internal/httpapi/body.go#L74). Lists need a bound before querying: the relations list is a separate pagination example with a default of 50, maximum 200, a tenant/query-bound cursor and `limit+1`, at [internal/relations/module.go:268](../internal/relations/module.go#L268) and [internal/relations/module.go:301](../internal/relations/module.go#L301). The feature list is bounded by its compiled `Catalog`, not a precedent for an unbounded resource list.

**Mutate in one tenant transaction.** Carry the authenticated principal in the context, fence access changes, then make the live permission decision. Acquire resource locks before writing, and append events after all resource writes. The feature's extra advisory lock serializes tenant/project overrides and first insertion for the same key.

[internal/features/features.go:224](../internal/features/features.go#L224)

```go source=internal/features/features.go:224
	ctx = tenant.WithPrincipal(ctx, p)
	err := db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := authorize(ctx, tx, p, projectID, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "feature:"+p.TenantID+":"+key); err != nil {
			return err
		}
```

`db.InTenant` sets `aeon.tenant_id` transaction-locally, establishes project visibility, enforces keyed-agent admission and commits only after the callback succeeds. Do not substitute an unscoped `pool.Exec`, or treat an earlier `Require` as the final write authorization. Entry and commit are at [internal/db/db.go:95](../internal/db/db.go#L95); the access fence implementation is [internal/db/fences.go:43](../internal/db/fences.go#L43).

**Typed SQL is the current repository shape.** Pass parameters through pgx, scan into typed Go fields and propagate the error. `Write`, `Saved` and the snapshot type supply the boundary types; there is no generated sqlc query layer to invent for this ticket.

[internal/features/features.go:252](../internal/features/features.go#L252)

```go source=internal/features/features.go:252
		var revision int64
		err = tx.QueryRow(ctx, `INSERT INTO features(tenant_id,key,project_id,enabled)
            VALUES($1::uuid,$2,$3::uuid,$4)
            ON CONFLICT (tenant_id,key,project_id) DO UPDATE SET enabled=EXCLUDED.enabled,
                revision=features.revision+1,updated_at=clock_timestamp() RETURNING revision`,
			p.TenantID, key, nullProject(projectID), in.Enabled).Scan(&revision)
		if err != nil {
			return err
		}
```

`Save` then appends `feature.updated` with before/after snapshots in the same transaction at [internal/features/features.go:262](../internal/features/features.go#L262). Its subsequent `load` is a read, not a new resource lock. A failed append rolls back the override; a no-op writes neither a row nor an event. HTTP success is sent only after `Save` returns without error, at [internal/features/http.go:115](../internal/features/http.go#L115). RLS is part of this example, not a substitute for its permission check (§4).

**Tests prove the risks.** Use the application role (`dbtest.DB.App`) for the behavior and RLS assertions; reserve the admin connection for setup, deliberate barriers and independent observations.

| Existing behavior test | What it establishes |
| --- | --- |
| [internal/features/features_test.go:156](../internal/features/features_test.go#L156) | Defaults/inheritance, explicit OFF, stale revision, no-op behavior and durable actor/snapshots. |
| [internal/features/features_test.go:231](../internal/features/features_test.go#L231) | Person-only writes, project visibility, cross-tenant reads/writes and no event for a denied mutation. |
| [internal/features/features_test.go:292](../internal/features/features_test.go#L292) | Malformed input, one success/one revision conflict, and override rollback when an event trigger fails. Its race is a result check; use the next test when claiming lock ordering. |
| [internal/features/features_test.go:345](../internal/features/features_test.go#L345) | A successful precheck is paused, a role revocation commits or holds its fence, then the write must wait and fail for `missing_role_permission`, with no feature/event left behind. No sleep establishes the interleaving. |

For a new endpoint, name its risk and add the smallest behavior test that proves it. Use barriers/injected clocks for concurrency/time claims, keep fixture rows for the assertion, and register tests through the tier writer. Do not duplicate this entire matrix for a small unrelated endpoint.

## 4. Worked migration: expand, isolate, backfill, then contract

Numbers are global ordering, not package ownership. Historical bands are useful landmarks: `0001`–`0021` foundations/auth, `0100`–`0103` nodes/events/search/views, `0200`–`0203` agent work, `0300`–`0304` early extensions (including now-retired Flow), `0400`–`0403` business, followed by interleaved additions through the current `1xxx` series. They are not ranges from which a worker may allocate. The runner embeds and sorts filenames at [internal/db/migrate.go:26](../internal/db/migrate.go#L26) and [internal/db/migrate.go:723](../internal/db/migrate.go#L723).

Use only the coordinator-reserved number/range in the brief and ledger, above the latest released maximum. Never fill a historical gap or renumber a collision yourself. New files are `NNNN_name.sql`; published files stay immutable. This guide intentionally offers no “next free number.” The guard's filename, duplicate and immutability checks start at [scripts/check-migrations.mjs:283](../scripts/check-migrations.mjs#L283).

**Simple expansion.** Copy the nullable-column shape from the assistant pull-health expansion. It leaves older assistant writers valid; its indexes support the read paths without rewriting bindings.

[internal/db/migrations/1436_assistant_pull_health.sql:5](../internal/db/migrations/1436_assistant_pull_health.sql#L5)

```sql source=internal/db/migrations/1436_assistant_pull_health.sql:5
ALTER TABLE principals ADD COLUMN assistant_last_pull_at timestamptz;

CREATE INDEX inbox_assistant_pull_principal ON inbox_message_targets
    (tenant_id, principal_id) WHERE target_kind='pull' AND enabled;
```

**New tenant table.** The feature migration is the concrete table/RLS counterpart to §3. It uses a tenant key, a revision and a composite project reference. Its tenant policy fails closed when the setting is absent or empty; `FORCE` matters because the application role owns tables. Both read and write conditions are required.

[internal/db/migrations/1110_feature_flags.sql:17](../internal/db/migrations/1110_feature_flags.sql#L17)

```sql source=internal/db/migrations/1110_feature_flags.sql:17
ALTER TABLE features ENABLE ROW LEVEL SECURITY;
ALTER TABLE features FORCE ROW LEVEL SECURITY;
CREATE POLICY features_tenant ON features
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
```

That file also installs a restrictive project-visibility policy at [internal/db/migrations/1110_feature_flags.sql:22](../internal/db/migrations/1110_feature_flags.sql#L22). Tenant isolation alone does not protect projects. A private project resource needs an appropriate project policy as well as handler authorization. Add DSAR classifications/locators for every new table/column in `internal/dsar/inventory.json`; identities and external identifiers are personal data. The inventory is enforced by [internal/dsar/registry_test.go:126](../internal/dsar/registry_test.go#L126).

**Expand-safe policy.** Apply AGENTS.md's writer-compatibility rule first. The syntactic allowlist is necessary evidence, not a proof that an old binary can still write. Its exact implementation is [scripts/check-migrations.mjs:207](../scripts/check-migrations.mjs#L207) and the detailed policy is [internal/db/migrations/README.md:91](../internal/db/migrations/README.md#L91).

| Shape | Rule for new work |
| --- | --- |
| New tables; indexes, including unique/concurrent indexes; types, functions, policies and triggers | Additive declarations can be classified as expansion. Check their effect on older writers: a new trigger/policy/unique index may still reject previously valid operations. New-table constraints must not impose incompatible requirements on existing writers. |
| `ADD COLUMN` | Prefer nullable. The classifier also accepts a constant default, including `NOT NULL` with a non-null constant. That is not blanket authorization to add a requirement older writers can violate. Function/expression defaults are outside this allowlist. |
| Constraints | The classifier recognizes `ADD CONSTRAINT … NOT VALID` and validation. Such a constraint still checks new writes. A CHECK/FK older writers could violate needs staged contract treatment. A narrow same-statement FK exception requires a fresh unconstrained nullable built-in UUID component; see the policy rather than generalizing it. |
| Backfills, `COMMENT ON`, `GRANT`, `SET LOCAL` | The classifier accepts INSERT/UPDATE. Backfills must preserve old behavior and data, be tenant-scoped, bounded and safe to resume. A destructive UPDATE still needs contract treatment. |
| DROP, RENAME, type changes, replacing functions/policies, setting an existing column NOT NULL, DO/dynamic SQL, unknown forms | Not expansion. Ship the additive replacement first, backfill/move clients, then use a later contract change with released expansion evidence. |

The required standalone header has the shape `-- aeon:contract-phase TICKET-N expanded-in=vYYMMDDhhmmss.0.0 expansion-migration=NNNN_name.sql`; use actual ticket/tag/filename evidence, not placeholders. The named release must be a real ancestor of the previous release and contain the expansion. Exact-byte historical exceptions are not templates or worker-granted approvals. The previous-binary compatibility job remains required even when static classification passes; evidence validation is [scripts/check-migrations.mjs:257](../scripts/check-migrations.mjs#L257) and [scripts/check-migrations.mjs:271](../scripts/check-migrations.mjs#L271).

**Backfill deliberately.** For an additive seed, the existing role-permission migration is idempotent and retains current grants/key scopes:

[internal/db/migrations/1061_agent_roles_models_read.sql:7](../internal/db/migrations/1061_agent_roles_models_read.sql#L7)

```sql source=internal/db/migrations/1061_agent_roles_models_read.sql:7
INSERT INTO role_permissions(tenant_id, role_id, permission)
SELECT DISTINCT b.tenant_id, b.role_id, 'models.read'
FROM role_bindings b
JOIN principals p ON p.tenant_id = b.tenant_id AND p.id = b.principal_id
JOIN roles r ON r.tenant_id = b.tenant_id AND r.id = b.role_id
WHERE p.kind = 'agent' AND NOT r.builtin
ON CONFLICT DO NOTHING;
```

The runner explicitly enters each tenant before applying this particular file; this is a named special case, not automatic behavior for arbitrary backfills. See [internal/db/migrate.go:125](../internal/db/migrate.go#L125) and [internal/db/agent_role_models_migration_test.go:15](../internal/db/agent_role_models_migration_test.go#L15). Plan large backfills as bounded/resumable work rather than copying its small-tenant enumeration. Preserve the global tenant → tree → rows → blob → event-counter order; use `db.LockTenant` for new fences rather than copying historical inline `FOR UPDATE`.

Ordinary files and their migration record commit together under the migration advisory lock. A hot-table concurrent index is its own one-statement file with `-- aeon:no-transaction` on the first line; the runner owns its retry/invalid-index handling. Copy [internal/db/migrations/1370_telemetry_retention_index.sql:1](../internal/db/migrations/1370_telemetry_retention_index.sql#L1), not manual transaction wrappers. Run `node scripts/check-migrations.mjs` and relevant migration/RLS tests in the assigned lane; hosted compatibility checks run the previous released binary against the expanded schema. Full runner/recovery rules are in [migration documentation](../internal/db/migrations/README.md).

## 5. Worked event type: reversible shared project groups

Project groups make the whole lifecycle visible: named types, complete resource snapshots, a mutation append, resource-specific Undo and composition-root registration. Choose it for those seams, not as a pagination template for a resource list.

[internal/projectgroups/module.go:29](../internal/projectgroups/module.go#L29)

```go source=internal/projectgroups/module.go:29
const (
	EventCreated  = "project_group.created"
	EventUpdated  = "project_group.updated"
	EventDeleted  = "project_group.deleted"
	EventAssigned = "project_group.assigned"
)
```

Creation has already locked the tenant, checked current authority, assigned projects and loaded the final group before appending. Its `Previous` assignments capture what Undo must restore; a response delta alone would lose that information.

[internal/projectgroups/module.go:314](../internal/projectgroups/module.go#L314)

```go source=internal/projectgroups/module.go:314
		e, err := events.Append(ctx, tx, p, events.Change{Type: EventCreated, After: snapshot{Group: group, Previous: taken}})
		if err != nil {
			return err
		}
		out = result{Group: &group, EventID: &e.ID}
```

Use `events.Append`/`events.Change` for new ordinary domain writers. It takes the caller's transaction and never commits independently. The database allocates the tenant-local position and publishes only a wake-up hint on commit; consumers replay committed rows. See [internal/events/writer.go:79](../internal/events/writer.go#L79), [internal/db/migrations/0101_relations_events.sql:67](../internal/db/migrations/0101_relations_events.sql#L67) and [internal/db/migrations/0101_relations_events.sql:89](../internal/db/migrations/0101_relations_events.sql#L89). Finish every resource write/lock before the first append. A batch prepares all mutations, then flushes events, rather than alternating writes and appends.

New dotted event names normally need no enum migration: the persisted type uses a naming pattern. Choose bounded, non-secret snapshots and decide visibility intentionally. `node_id` routes project-scoped history; nil in the shared-group example denotes workspace metadata, not permission to publish private project payloads. Check current RLS/read redaction before adding fields.

**Undo belongs to the resource.** Return handlers keyed by event type, re-authorize against current state, lock the resource, reject stale state, then return the compensating `events.Change`:

[internal/projectgroups/undo.go:20](../internal/projectgroups/undo.go#L20)

```go source=internal/projectgroups/undo.go:20
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{
		EventCreated:  undoCreated,
		EventUpdated:  undoUpdated,
		EventDeleted:  undoDeleted,
		EventAssigned: undoAssigned,
	}
}
```

`undoCreated` compares the current group's name, position and memberships with the recorded creation before deleting/restoring at [internal/projectgroups/undo.go:38](../internal/projectgroups/undo.go#L38). The common Undo endpoint fences tenant/tree, verifies actor or `events.undo_other`, ensures single Undo, calls the handler and appends `undo_of`; it does not edit the original log entry. See [internal/events/module.go:313](../internal/events/module.go#L313). Registration occurs in [cmd/aeon/serve.go:511](../cmd/aeon/serve.go#L511). Do not promise reversibility for an event without a registered handler.

The behavioral example [internal/projectgroups/projectgroups_test.go:129](../internal/projectgroups/projectgroups_test.go#L129) verifies restored memberships, no-op suppression, stale/single Undo and the resulting compensating event sequence. Add stream consumers only when the feature needs them: browser updates use [web/src/lib/goalsLive.ts:36](../web/src/lib/goalsLive.ts#L36); the agent `/events/subscribe` feed is a separate fail-closed allowlist with resource-read permissions and no event snapshots at [internal/events/subscription_topics.go:14](../internal/events/subscription_topics.go#L14). A new event is not automatically a new agent subscription topic. Ephemeral heartbeat/control frames and telemetry retention are separate exceptions (§9).

## 6. Worked authorization: the current target, inside the write

Authentication answers who called. Route admission screens the operation. The final transaction must still decide whether that actor may mutate this target now. A role label, cached `/me`, API key scope, feature flag or completed precheck is insufficient by itself. `RequireTx` evaluates registered permissions, live bindings and agent ceilings; pass the same authenticated principal through `db.InTenant` so keyed admission runs before handler locks. See [internal/authz/require.go:119](../internal/authz/require.go#L119) and [internal/db/db.go:67](../internal/db/db.go#L67).

The small example is the feature service's `authorize`, called after `db.LockTenant` in §3. Settings changes require a person and workspace `settings.manage`; a supplied project is also checked as a live visible project. Copy the sequence, with the new resource's real scope:

[internal/features/features.go:137](../internal/features/features.go#L137)

```go source=internal/features/features.go:137
func authorize(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, admin bool) error {
	permission, scope := "nodes.read", authz.Scope{ProjectID: projectID, AnyProject: projectID == ""}
	if admin {
		if p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		permission, scope = "settings.manage", authz.Scope{}
	}
	if err := authz.RequireTx(ctx, tx, p, permission, scope); err != nil {
		return err
	}
	if projectID != "" {
		var found bool
		err := tx.QueryRow(ctx, `SELECT true FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
            WHERE n.tenant_id=$1::uuid AND n.id=$2::uuid AND k.slug='project' AND n.deleted_at IS NULL`, p.TenantID, projectID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return fault(404, "project not found")
		}
		return err
	}
	return nil
}
```

For node-specific writes, acquire tenant then tree if needed, lock/read the current node, resolve its current project and call `RequireTx` with that scope before writing. For a move or edge across projects, check each affected project's authority (`RequireInProjects` or the established endpoint resolver), not just the route's initial project. `Scope{AnyProject:true}` is for registered reads confined by project RLS, not write-anywhere authority. See [internal/authz/require.go:25](../internal/authz/require.go#L25) and [internal/authz/require.go:101](../internal/authz/require.go#L101).

Use the shared fence primitives, whose canonical SQL is observable by lock-order tests: [internal/db/fences.go:17](../internal/db/fences.go#L17) and [internal/db/fences.go:69](../internal/db/fences.go#L69). The order is tenant → tree → node/record rows → one sorted tenant/hash blob batch → event counter last. Prefer `FOR NO KEY UPDATE` for the tenant fence so FK share locks can proceed. Blob batching is [internal/attachments/storage.go:335](../internal/attachments/storage.go#L335). `RequireTx` does not by itself acquire every resource/access-change lock.

The strongest test excerpt comes from the feature revocation test. It proves the write actually blocked on the revocation transaction, then rejects the exact authorization reason and unchanged data/audit. The timeout only guards against a hang.

[internal/features/features_test.go:389](../internal/features/features_test.go#L389)

```go source=internal/features/features_test.go:389
			if holdRevocation {
				barrier.Release()
				if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, revoke.Conn().PgConn().PID(), done); lock != "transactionid" {
					t.Fatalf("feature writer did not wait for access fence: %q", lock)
				}
			}
```

The full setup, commit and reason/data assertions are in [internal/features/features_test.go:345](../internal/features/features_test.go#L345). Reuse `dbtest.BarrierPool`, `BlockedOrDone` and injected clocks rather than sleeps or elapsed-time assertions.

Assistant execution adds a ceiling to these ordinary grants. [internal/authz/assistant_policy.go:18](../internal/authz/assistant_policy.go#L18) checks scopes without importing the assistant domain into authz, while [internal/db/assistant_visibility.go:17](../internal/db/assistant_visibility.go#L17) intersects project/node RLS visibility for ordinary list reads. Neither grants authority. On-behalf-of services explicitly holding all-project visibility still check the target assistant's policy at their final admission.

[internal/assistants/enforcement.go:68](../internal/assistants/enforcement.go#L68) returns policy receipts and pending audit while atomically reserving the assistant's weekly allowance. Its callers already own the access fence; they finish resource writes before appending those changes. [internal/recurrences/assistant_policy.go:15](../internal/recurrences/assistant_policy.go#L15) installs that evaluator in routine admission. [internal/recurrences/assistant_delegation.go:56](../internal/recurrences/assistant_delegation.go#L56) reuses a person-granted routine and exact lead/key/session/instance bindings instead of creating another executor. See [assistant policy enforcement](features/assistant-policy-enforcement.md) for the accounting and threat boundaries.

## 7. Worked Vue view: Goals and its shared edit sheet

Use `GoalsView.vue` as the composition example and `GoalSheet.vue` for a small complete write surface. They use Vue 3 `<script setup lang="ts">`, typed library clients, shared controls and identity-scoped results. This selection is intentionally narrower than copying an entire large page. Follow [UI conventions](ui-conventions.md) and the ticket's approved HTML; the guide is not a new design specification.

**Data and identity.** The sheet captures the editing goal when opened; submission snapshots form values and the record ID before starting work. `useIdentityScope` drops results on person/tenant/access changes or disposal, checking immediately before each state-changing continuation. A later async stage must use `after` again, not await a checked promise and mutate in another turn. Server revision/precondition requirements still apply where the resource API exposes them; do not invent a revision field absent from the contract.

[web/src/components/goals/GoalSheet.vue:37](../web/src/components/goals/GoalSheet.vue#L37)

```ts source=web/src/components/goals/GoalSheet.vue:37
function submit() {
  if (!valid.value) return
  const snapshot = { title: title.value.trim(), place: place.value, lands: lands.value }
  busy.value = true; error.value = ''
  if (editing) {
    const id = editing.id, before = { title: editing.title, lands: editing.eta?.source === 'agent' ? landsDay(editing.eta.at) : '' }
    const change = { ...(snapshot.title !== before.title ? { title: snapshot.title } : {}), ...(snapshot.lands !== before.lands ? { lands: snapshot.lands } : {}) }
    if (!Object.keys(change).length) { busy.value = false; emit('close'); return }
    void scope.run(({ after }) => after(updateGoal(id, change), () => emit('saved', id)), { failed: failed, settled: () => { busy.value = false } })
    return
  }
  void scope.run(({ after }) => after(createGoal(snapshot.title, snapshot.place === WORKSPACE ? null : snapshot.place, snapshot.lands), result => emit('created', result.id)), { failed, settled: () => { busy.value = false } })
}
```

Use [web/src/lib/useIdentityScope.ts:12](../web/src/lib/useIdentityScope.ts#L12) and [web/src/lib/identityScope.ts:57](../web/src/lib/identityScope.ts#L57). The parent mounts the sheet for its selected goal at [web/src/components/goals/GoalsView.vue:428](../web/src/components/goals/GoalsView.vue#L428). Record-bound Undo is illustrated by deletion at [web/src/components/goals/GoalsView.vue:222](../web/src/components/goals/GoalsView.vue#L222): it captures the goal ID and returned event ID, and reuses the identity scope when Undo runs. UI permission checks choose available controls; the server remains authoritative.

**Shared components and materials.** `GoalSheet` composes `ProjectSettingsSheet`, `TargetDateField` and `KeyCap`; `GoalsView` reuses `RowMenu`, `RelationPicker` and goal rows. Reuse those components where their behavior fits. Shared tokens/base styles load once in [web/src/main.ts:4](../web/src/main.ts#L4). The sheet uses `var(--ink-3)`, `var(--surface-2)`, shared `.field`/`.btn` controls and SVG icons rather than locally invented colors or text symbols. Build both light and dark from tokens. Full tints, hairlines and type weight carry emphasis; no colored edge accents, decorative glow/frame/blob or grey-pill soup.

**The controls stay put.** The shared sheet places its scrollable body between header and actions, including phone safe areas. Errors grow inside the body; they do not move the footer. This is pattern B, appropriate for a full-height sheet. For a short task dialog, pattern A is a fixed top anchor with controls above downward-growing content; do not pad it to a fixed height. A reusable current pattern-A frame is [web/src/components/rules/RulesDialog.vue:48](../web/src/components/rules/RulesDialog.vue#L48); reuse its frame rather than inferring that every older consumer implements today's keyboard rules.

[web/src/components/settings/ProjectSettingsSheet.vue:42](../web/src/components/settings/ProjectSettingsSheet.vue#L42)

```vue source=web/src/components/settings/ProjectSettingsSheet.vue:42
      <div class="sh-body"><slot /></div>
      <footer class="sh-foot"><slot name="actions" /></footer>
```

The corresponding grid/scroll/footer CSS is at [web/src/components/settings/ProjectSettingsSheet.vue:50](../web/src/components/settings/ProjectSettingsSheet.vue#L50). Keep selector rows fixed and put changing explanations in a reserved slot below. The concrete overlay slot technique is [web/src/components/agents/PauseDialog.vue:63](../web/src/components/agents/PauseDialog.vue#L63); it reserves content size, not a padded dialog height. Loading labels must fit without resizing adjacent actions; a content-sized hidden longest label is used in [web/src/components/rules/DoctrineProposalDialog.vue:58](../web/src/components/rules/DoctrineProposalDialog.vue#L58).

**Keyboard follows the same owner.** `ProjectSettingsSheet` uses the shared action registry and a predicate for its own modal; Esc first blurs a field and then closes the sheet, while submit uses the platform-specific modifier. Registrations are disposed on unmount and focus returns to the opener.

[web/src/components/settings/ProjectSettingsSheet.vue:18](../web/src/components/settings/ProjectSettingsSheet.vue#L18)

```ts source=web/src/components/settings/ProjectSettingsSheet.vue:18
const keys = registerOverlay ? [] : [
  onAction('project.settings.dismiss', event => {
    if (props.escape?.()) return
    if (shortcutTyping(event.target)) { (event.target as HTMLElement).blur(); dialog.value?.focus({ preventScroll: true }) }
    else emit('close')
  }, { when: owns }),
  onAction('project.settings.submit', () => emit('submit'), { when: event => owns(event) && settingsSubmitKey(event) }),
]
```

`settingsSubmitKey` rejects the wrong platform modifier, extra modifiers and composition at [web/src/lib/settingsOverlays.ts:17](../web/src/lib/settingsOverlays.ts#L17). Page shortcuts register with `onAction` and are disposed with their view, as in [web/src/components/goals/GoalsView.vue:314](../web/src/components/goals/GoalsView.vue#L314). The registry excludes typing contexts, active overlays and native browser/OS modifier shortcuts at [web/src/lib/shortcuts.ts:133](../web/src/lib/shortcuts.ts#L133); `KeyCap` uses the same platform distinction at [web/src/components/KeyCap.vue:10](../web/src/components/KeyCap.vue#L10). Put the keycaps on their action buttons.

**Measure the behavior you claim.** `controlStability` checks positive-sized controls, at least one interaction, scroll-adjusted coordinates, ±0.5 px bounds and horizontal overflow. Include actions, selectors, selector groups and the clicked row before/after every option/series step; include frame height for phone/already-scrolling sheets, not downward-growing pattern A. The actual reusable guard is [web/tests/control-stability.ts:27](../web/tests/control-stability.ts#L27). A current Goals invocation is:

[web/tests/goals-create-link.spec.ts:165](../web/tests/goals-create-link.spec.ts#L165)

```ts source=web/tests/goals-create-link.spec.ts:165
      const guard = await controlStability(page, controls)
      const sheet = page.locator('dialog.project-settings-sheet')
      await guard.check(async () => { await (await newGoalEntry(page, phone)).click(); await expect(sheet).toBeVisible() })
```

That excerpt measures the underlying toolbar/row while opening the sheet; it does not prove every selector inside the sheet is stable. The same test calls `guard.done()` after closing. For new/changed UI, add the internal controls to the measured set, verify phone 390 px plus 1024/1440 light/dark and long German labels, and retain genuine interaction/identity-race assertions. Follow the assigned remote/browser lane and visual CI rules; screenshots use `testInfo.outputPath()` and are artifacts, not committed build output.

## 8. Worked anti-corruption boundary: OpenRouter key checks

An outside system's DTOs, errors and authority do not become Aeon's domain model. `internal/openrouter.Client.CheckKey` is a compact example: it takes an injected HTTP client, calls a narrowly chosen endpoint, decodes a private provider DTO, validates it, then returns a small internal `Credits` projection. No prompt or model completion is needed for this check. Use it as a boundary shape, not as authorization to obtain or call real credentials.

**Bound transport before decoding.** The helper adds a four-second context deadline, refuses redirects even for an injected client, reads `maximum+1` and refuses oversized/invalid responses. Transport and provider text never enter its errors.

[internal/openrouter/client.go:66](../internal/openrouter/client.go#L66)

```go source=internal/openrouter/client.go:66
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrKey
	}
	if res.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maximum+1))
	if err != nil || int64(len(raw)) > maximum || json.Unmarshal(raw, into) != nil {
		return ErrUnavailable
	}
	return nil
}
```

The deadline starts at [internal/openrouter/client.go:48](../internal/openrouter/client.go#L48). HTTP 401/403 becomes the stable `ErrKey`; other failures become `ErrUnavailable`, preserving a useful distinction without reflecting bodies, URLs, headers or account labels.

**Translate semantic meaning as well as field names.** The provider's `limit_remaining` is a key-cap reading, not proof of account balance. The private decoded shape and request are at [internal/openrouter/client.go:116](../internal/openrouter/client.go#L116); internal `Credits.Valid` rejects negative/non-finite readings. Its null/zero distinction is explicit:

[internal/openrouter/client.go:94](../internal/openrouter/client.go#L94)

```go source=internal/openrouter/client.go:94
func (c Credits) KeyRemaining() *float64 {
	if c.Limit == nil {
		return nil
	}
	if *c.Limit == 0 {
		zero := 0.0
		return &zero
	}
	return c.Remaining
}
```

The seam is tested with fake HTTP servers at [internal/openrouter/client_test.go:58](../internal/openrouter/client_test.go#L58), [internal/openrouter/client_test.go:96](../internal/openrouter/client_test.go#L96) and [internal/openrouter/client_test.go:110](../internal/openrouter/client_test.go#L110). They check projection, redirect refusal, safe errors and unknown versus exhausted credit without an outside call. For new timeout/concurrency tests use injected clocks or barriers; the older elapsed-time test in this package is not the timing template.

The same boundary choices appear at other seams: GitHub translates HTTP statuses into content-free typed metadata and distinguishes confirmed absence from unavailable/partial results in [internal/crossreview/github.go:85](../internal/crossreview/github.go#L85) and bounds responses in [internal/crossreview/github.go:106](../internal/crossreview/github.go#L106). OIDC discovery/JWKS has a byte-limited transport and application deadline at [internal/auth/module.go:70](../internal/auth/module.go#L70) and [internal/auth/module.go:1033](../internal/auth/module.go#L1033). Workspace model configuration resolves an `embedding.Provider` rather than spreading provider JSON through indexing/search, at [internal/modelprovider/client.go:208](../internal/modelprovider/client.go#L208).

Perform network work outside the final mutation transaction where the feature allows it. Snapshot the intended target/revision; after the response, enter a fresh tenant transaction, acquire current fences, re-read/re-authorize and reject stale results before writing. A successful vendor response never substitutes for the current permission, person approval, generation or ownership check.

## 9. Known exceptions and legacy shapes not to copy

These are navigation warnings found while checking the examples. They remain untouched by this documentation change; presence in the import graph, schema history or an old comment does not make a shape the recommendation.

| Where | What exists / what to do for new work |
| --- | --- |
| [internal/journey/module.go:18](../internal/journey/module.go#L18); [internal/stagehandoff/module.go:21](../internal/stagehandoff/module.go#L21); [internal/nodes/journey_move.go:32](../internal/nodes/journey_move.go#L32) | Journey and stage routes are compatibility tombstones; some membership/move helpers and release storage retain legacy names. Route retirement work to its owner. Current release/delivery features belong in their active packages; never restore historical Flow actions. |
| [internal/httpapi/server.go:152](../internal/httpapi/server.go#L152); [internal/fromclassic/module.go:6](../internal/fromclassic/module.go#L6) | Classic migration/redirect compatibility is retained. Classic is retired. The PPM personal instance and the separate PMA business context do not share ticket/credential authority. |
| [internal/db/migrations/0100_nodes.sql:35](../internal/db/migrations/0100_nodes.sql#L35); [internal/db/migrations/1215_one_work_kind.sql:1](../internal/db/migrations/1215_one_work_kind.sql#L1) | Old seeds and historic snapshots contain separate epic/ticket/task kinds; later work migrations supersede the live model. Preserve released bytes and old parsers; choose the current `work` kind and shape-derived vocabulary for new creation. |
| [internal/nodes/events.go:41](../internal/nodes/events.go#L41); [internal/views/events.go:14](../internal/views/events.go#L14) | Multiple historical transactional writer interfaces remain. The nodes comment still says the common writer is forthcoming although `events.Append` exists. New ordinary domain writers use the common append contract; retain existing injectable seams when extending their owning package. |
| [internal/authz/event.go:28](../internal/authz/event.go#L28) | Authz locally inserts its events to avoid the `events → authz → events` cycle. This is a documented dependency boundary, not a reason to duplicate append SQL in every new package. |
| [internal/events/retention.go:26](../internal/events/retention.go#L26); [internal/events/telemetry.go:17](../internal/events/telemetry.go#L17) | Qualifying old telemetry is pruned through a dedicated retention path, and heartbeat notifications can be ephemeral. These are narrow exceptions to durable append-only domain history; never generalize them into deleting audit events or treating NOTIFY as the source of truth. |
| [internal/events/writer.go:66](../internal/events/writer.go#L66); [internal/authz/routes_features.go:6](../internal/authz/routes_features.go#L6) | Process-wide event guard and init-time permission/route registries exist despite the general preference against package global state. They are existing initialization seams, not a state/cache pattern for new features. |
| [scripts/check-migrations.mjs:158](../scripts/check-migrations.mjs#L158); [scripts/check-migrations.mjs:207](../scripts/check-migrations.mjs#L207); [internal/db/migrate.go:125](../internal/db/migrate.go#L125) | The classifier permits syntactic NOT VALID constraints and UPDATEs; policy still forbids breaking old writers/destructive backfills without contract treatment. The small historical backfill also has inline `FOR UPDATE`; new access fences use the shared FK-compatible helper. Exact-byte policy exceptions do not authorize a new exception. |
| [internal/features/features.go:253](../internal/features/features.go#L253); [internal/attachments/storage.go:36](../internal/attachments/storage.go#L36) | ADR stack wording describes sqlc/S3 direction; the inspected implementation uses handwritten typed pgx SQL and filesystem blob storage. Follow current working seams; do not claim a generator or S3 backend ships here. |
| [web/src/components/knowledge/KnowledgeCreateDialog.vue:75](../web/src/components/knowledge/KnowledgeCreateDialog.vue#L75); [web/src/components/knowledge/KnowledgeCreateDialog.vue:147](../web/src/components/knowledge/KnowledgeCreateDialog.vue#L147) | Older dialog code accepts either modifier and scrolls the whole desktop card, with feedback before its footer. It is not the current keyboard/no-shift template. Use the scoped registry, shared sheet/frame and measured controls in §7. |
| [internal/openrouter/client_test.go:46](../internal/openrouter/client_test.go#L46) | An older provider test measures real elapsed time. New time-dependent claims follow the barrier/injected-clock policy; copy the projection/redirect tests for this boundary instead. |

## Keeping this guide usable

When changing a cited implementation, update its `path:line` links and copy the current contiguous source excerpt, including indentation. Do not turn an example into untested pseudocode or silently bless an exception by moving it into the examples. Reconfirm the relevant dependency direction with `go list -deps` and explain any new exception in the owning ticket.

[internal/reportercontract/architecture_guide_test.go:18](../internal/reportercontract/architecture_guide_test.go#L18) checks that every local source citation resolves to an existing line and that every attributed Go/SQL/YAML/Vue/TypeScript/CSS excerpt matches the source bytes at its stated line. Negative fixtures reject missing files, bad line numbers, path traversal, stale/out-of-range/unattributed/unclosed excerpts and empty coverage. Prose, full import direction and behavioral correctness still require human/coordinator review.

The guard runs through the existing reporter-contract Go test lane, registered as ESSENTIAL in the tier manifest. To run the targeted check in a test environment:

```sh
go test -p 2 ./internal/reportercontract/ -run '^TestArchitectureGuideSources$' -count=1
```

For endpoint/schema changes, run the full reporter-contract package after regenerating OpenAPI. For migrations and UI, use the checks named above and AGENTS.md/worker-common's assigned hosted or remote lanes. The coordinator owns the cross-family review, pushes and deployment; this guide adds no new worker approval or release authority.
