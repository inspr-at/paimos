-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1456, concept r3: goals and outcomes remain ordinary nodes. This
-- expansion creates no kinds, goals, outcomes or sample data. Kind seeding
-- belongs to the first authorized create transaction.
-- The pinned policy exception covers the relation CHECK widen, event policy
-- extension and response sequence. Existing relation types remain valid.
SET LOCAL lock_timeout = '5s';

CREATE TABLE goal_update_receipts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    actor_principal_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 200),
    method text NOT NULL CHECK (method IN ('POST', 'PATCH')),
    -- Historical identity: no FK, so a later node purge is not blocked. The
    -- write path must resolve and authorize the current target before replay.
    target_node_id uuid NOT NULL,
    request_sha256 bytea NOT NULL CHECK (length(request_sha256) = 32),
    response jsonb NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    event_id bigint NOT NULL CHECK (event_id >= 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, actor_principal_id, idempotency_key),
    FOREIGN KEY (tenant_id, actor_principal_id) REFERENCES principals(tenant_id, id)
);
ALTER TABLE goal_update_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_update_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY goal_update_receipts_tenant ON goal_update_receipts
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- Gaps, including rolled-back reads, are intentional. This orders snapshots
-- with the same visible event watermark; it does not allocate event ids.
CREATE SEQUENCE goal_response_seq AS bigint;

-- RLS cannot push non-leakproof JSON/array predicates into their indexes.
-- Normalize immutable references into plain UUID keys instead. This is an
-- index projection only: snapshots and visibility still come from events.
CREATE TABLE goal_event_refs (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    node_id uuid NOT NULL,
    event_id bigint NOT NULL,
    type text NOT NULL,
    at timestamptz NOT NULL,
    PRIMARY KEY(tenant_id,node_id,event_id)
);
CREATE INDEX goal_event_refs_node_at_idx ON goal_event_refs(tenant_id,node_id,at,event_id);
ALTER TABLE goal_event_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_event_refs FORCE ROW LEVEL SECURITY;
CREATE POLICY goal_event_refs_tenant ON goal_event_refs
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY goal_event_refs_visible ON goal_event_refs AS RESTRICTIVE FOR SELECT
    USING (EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=goal_event_refs.tenant_id AND e.id=goal_event_refs.event_id));
CREATE FUNCTION aeon_goal_event_refs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.type LIKE 'node.%' OR NEW.type LIKE 'import.%' OR NEW.type LIKE 'harness.%'
        OR NEW.type IN ('goal.items_changed','goal.updated') THEN
        INSERT INTO goal_event_refs(tenant_id,node_id,event_id,type,at)
            SELECT NEW.tenant_id,ref,NEW.id,NEW.type,NEW.at
            FROM (SELECT DISTINCT unnest(NEW.node_refs||ARRAY[NEW.node_id]) AS ref) refs
            WHERE ref IS NOT NULL AND ref<>'00000000-0000-0000-0000-000000000000'::uuid;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER events_goal_refs AFTER INSERT ON events FOR EACH ROW EXECUTE FUNCTION aeon_goal_event_refs();
-- Previous writers also feed this projection through the event trigger. Seed
-- existing references tenant by tenant, without rewriting any event or node.
DO $$
DECLARE
    target_tenant uuid;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_visible text := current_setting('aeon.visible_projects',true);
BEGIN
    FOR target_tenant IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id',target_tenant::text,true);
        PERFORM set_config('aeon.visible_projects','*',true);
        INSERT INTO goal_event_refs(tenant_id,node_id,event_id,type,at)
            SELECT e.tenant_id,refs.ref,e.id,e.type,e.at FROM events e
            CROSS JOIN LATERAL (SELECT DISTINCT unnest(e.node_refs||ARRAY[e.node_id]) AS ref) refs
            WHERE e.tenant_id=target_tenant AND refs.ref IS NOT NULL
                AND refs.ref<>'00000000-0000-0000-0000-000000000000'::uuid
                AND (e.type LIKE 'node.%' OR e.type LIKE 'import.%' OR e.type LIKE 'harness.%'
                    OR e.type IN ('goal.items_changed','goal.updated'));
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
END;
$$;
-- Direct node snapshots also use plain identity/time index conditions.
CREATE INDEX events_goal_node_at_idx ON events(tenant_id,node_id,at,id)
    WHERE node_id IS NOT NULL;
-- UUID keysets remain indexable through RLS; numeric position comparisons in
-- row keysets are not leakproof and would rescan earlier siblings per page.
CREATE INDEX nodes_goal_scope_children_idx ON nodes(tenant_id,parent_id,id) WHERE deleted_at IS NULL;

-- Historical discovery also needs current edges. Use the same UUID child
-- ranges as the live scope so unrelated sibling trees cannot be rescanned.
-- Deleted edges come from recorded before/after snapshots in the next helper.
CREATE FUNCTION aeon_goal_current_children(p_parent uuid) RETURNS TABLE(id uuid)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    page_ids uuid[];
    last_id uuid;
    total integer := 0;
BEGIN
    SELECT array_agg(c.id ORDER BY c.id) INTO page_ids FROM (
        SELECT n.id FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
            AND n.parent_id=p_parent AND n.deleted_at IS NULL
            AND (n.parent_id,n.id)>=(p_parent,'00000000-0000-0000-0000-000000000000'::uuid)
            ORDER BY n.id LIMIT 128
    ) c;
    WHILE coalesce(cardinality(page_ids),0)>0 LOOP
        total := total+cardinality(page_ids);
        IF total>50000 THEN
            RAISE EXCEPTION 'series child scope budget exceeded' USING ERRCODE='54000';
        END IF;
        RETURN QUERY SELECT unnest(page_ids);
        EXIT WHEN cardinality(page_ids)<128;
        last_id := page_ids[cardinality(page_ids)];
        SELECT array_agg(c.id ORDER BY c.id) INTO page_ids FROM (
            SELECT n.id FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
                AND n.parent_id=p_parent AND n.deleted_at IS NULL
                AND (n.parent_id,n.id)>(p_parent,last_id) ORDER BY n.id LIMIT 128
        ) c;
    END LOOP;
END;
$$;

-- A reference probe either returns its complete bounded candidate set or
-- raises the history budget error. Never truncate before inspecting snapshots.
CREATE FUNCTION aeon_goal_history_refs(p_node uuid,p_type text,p_from timestamptz,p_until timestamptz)
RETURNS TABLE(event_id bigint) LANGUAGE plpgsql STABLE AS $$
DECLARE
    event_ids bigint[];
BEGIN
    SELECT array_agg(e.event_id) INTO event_ids FROM (
        SELECT r.event_id FROM goal_event_refs r
        WHERE r.tenant_id=current_setting('aeon.tenant_id')::uuid AND r.node_id=p_node
            AND r.type LIKE p_type AND r.at>p_from AND r.at<=p_until LIMIT 20001
    ) e;
    IF cardinality(event_ids)>20000 THEN
        RAISE EXCEPTION 'series reference history budget exceeded' USING ERRCODE='54000';
    END IF;
    RETURN QUERY SELECT unnest(event_ids);
END;
$$;

-- Bound matching records before unpacking bulk or wrapped node snapshots.
-- The prefix is still required: a tree can move away before the selected
-- range, and the last percentage change can predate that range. Both arms
-- are indexed reference lookups under the original invoker event RLS.
CREATE FUNCTION aeon_goal_history_children(p_parent uuid,p_start timestamptz,p_end timestamptz)
RETURNS TABLE(id uuid) LANGUAGE plpgsql STABLE AS $$
DECLARE
    event_ids bigint[];
BEGIN
    SELECT array_agg(e.event_id) INTO event_ids FROM (
        SELECT r.event_id FROM aeon_goal_history_refs(p_parent,'node.%','-infinity',p_start) r
        UNION ALL SELECT r.event_id FROM aeon_goal_history_refs(p_parent,'node.%',p_start,p_end) r
        UNION ALL SELECT r.event_id FROM aeon_goal_history_refs(p_parent,'import.%','-infinity',p_start) r
        UNION ALL SELECT r.event_id FROM aeon_goal_history_refs(p_parent,'import.%',p_start,p_end) r
        LIMIT 20001
    ) e;
    IF cardinality(event_ids)>20000 THEN
        RAISE EXCEPTION 'series parent history budget exceeded' USING ERRCODE='54000';
    END IF;
    RETURN QUERY SELECT DISTINCT coalesce(aeon_uuid_or_null(s.item->>'id'),e.node_id)
        FROM unnest(event_ids) ref(id) CROSS JOIN LATERAL (
            SELECT e.node_id,e.type,e.before,e.after FROM events e WHERE e.id=ref.id
                AND e.tenant_id=current_setting('aeon.tenant_id')::uuid OFFSET 0
        ) e
        CROSS JOIN LATERAL (VALUES(e.before),(e.after)) snapshots(data)
        CROSS JOIN LATERAL jsonb_array_elements(CASE
            WHEN e.type='node.bulk_changed' AND jsonb_typeof(snapshots.data->'items')='array' THEN snapshots.data->'items'
            WHEN e.type='node.project_moved' THEN jsonb_build_array(snapshots.data->'node')
            ELSE jsonb_build_array(snapshots.data) END) s(item)
        WHERE s.item->>'parent_id'=p_parent::text
            AND coalesce(aeon_uuid_or_null(s.item->>'id'),e.node_id) IS NOT NULL;
END;
$$;

-- Deleted outcomes retain their ref. Moves additionally retire the source
-- ref in the source goal's fields under the tenant/goal write fence.
CREATE UNIQUE INDEX nodes_outcome_ref_unique ON nodes
    (tenant_id, parent_id, (fields->>'outcome_ref'))
    WHERE fields ? 'outcome_ref';
CREATE UNIQUE INDEX nodes_goal_handed_over_unique ON nodes
    (tenant_id, parent_id)
    WHERE fields ? 'handed_over_at' AND deleted_at IS NULL;

ALTER TABLE node_relations DROP CONSTRAINT node_relations_type_check;
ALTER TABLE node_relations ADD CONSTRAINT node_relations_type_check
    CHECK (type IN ('blocks', 'relates', 'implements', 'cites', 'duplicates',
                   'customer_of', 'contact_for', 'serves', 'asks_for'));

-- Reading serves under the caller's relation RLS would silently omit a hidden
-- end and fail open. Collect the ids under widened PROJECT visibility only,
-- never tenant visibility, then resolve every end under the restored policies.
-- Invoker rights and nodes_rule_access still apply during the widened probe.
CREATE FUNCTION aeon_outcome_tickets_visible(p_id uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    prior text := current_setting('aeon.visible_projects', true);
    prior_path text := current_setting('aeon.outcome_visibility_path', true);
    path uuid[] := coalesce(NULLIF(prior_path, '')::uuid[], '{}'::uuid[]);
    tenant uuid := NULLIF(current_setting('aeon.tenant_id', true), '')::uuid;
    slug text;
    targets uuid[];
    target_id uuid;
    visible boolean;
BEGIN
    IF aeon_visible_all() THEN RETURN true; END IF;
    -- Reference-only targets may themselves be outcomes. Corrupt or cyclic
    -- graphs fail closed, without unbounded recursive RLS evaluation.
    IF p_id = ANY(path) OR cardinality(path) >= 32 THEN RETURN false; END IF;
    PERFORM set_config('aeon.outcome_visibility_path', (path || p_id)::text, true);
    PERFORM set_config('aeon.visible_projects', '*', true);
    SELECT k.slug INTO slug FROM nodes n
        JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
        WHERE n.tenant_id = tenant AND n.id = p_id;
    IF slug = 'outcome' THEN
        SELECT coalesce(array_agg(r.target_node_id), '{}'::uuid[]) INTO targets
        FROM (SELECT target_node_id FROM node_relations
              WHERE tenant_id = tenant AND source_node_id = p_id AND type = 'serves'
              LIMIT 21) r;
    END IF;
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
    -- INSERT checks can precede the row itself; a new outcome has no relations
    -- yet. Other kinds keep their pre-expansion behaviour.
    IF slug IS DISTINCT FROM 'outcome' THEN
        visible := true;
    ELSIF cardinality(targets) > 20 THEN
        visible := false;
    ELSE
        visible := true;
        -- A parameterized point lookup avoids hashing all visible outcomes,
        -- which would recursively invoke this policy for unrelated rows.
        FOREACH target_id IN ARRAY targets LOOP
            IF NOT EXISTS (SELECT 1 FROM nodes n
                           WHERE n.tenant_id = tenant AND n.id = target_id) THEN
                visible := false;
                EXIT;
            END IF;
        END LOOP;
    END IF;
    PERFORM set_config('aeon.outcome_visibility_path', coalesce(prior_path, ''), true);
    RETURN visible;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
    PERFORM set_config('aeon.outcome_visibility_path', coalesce(prior_path, ''), true);
    RAISE;
END;
$$;

-- Keep the ordinary-kind predicate visible to the planner. A CASE around the
-- whole policy estimates that half the nodes are hidden, even for ordinary
-- kinds, and turns recursive child probes into repeated sequential scans.
-- ARRAY(SELECT ...) is one InitPlan, without a NOT IN membership subplan.
-- The serves walk is needed only for outcomes under project-only visibility.
CREATE POLICY nodes_outcome_ticket_visibility ON nodes AS RESTRICTIVE
    USING ((SELECT aeon_visible_all())
        OR kind_id <> ALL (ARRAY(SELECT id FROM node_kinds WHERE slug = 'outcome'))
        OR aeon_outcome_tickets_visible(id))
    WITH CHECK ((SELECT aeon_visible_all())
        OR kind_id <> ALL (ARRAY(SELECT id FROM node_kinds WHERE slug = 'outcome'))
        OR aeon_outcome_tickets_visible(id));

-- Authorize the outcome once before collecting its bounded serves ends.
-- Relation RLS otherwise re-checks the source outcome (and all twenty ends)
-- for each edge, making a page quadratic in its targets. A visible outcome
-- already proves that every serves end is visible under the original scope.
-- Invoker tenant isolation remains active throughout; restore project scope
-- before returning any rows, including when the relation query fails.
CREATE FUNCTION aeon_outcome_serves(p_id uuid) RETURNS TABLE(target_node_id uuid)
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    prior text := current_setting('aeon.visible_projects', true);
    tenant uuid := NULLIF(current_setting('aeon.tenant_id', true), '')::uuid;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id = tenant AND n.id = p_id
        AND n.kind_id = ANY (ARRAY(SELECT id FROM node_kinds WHERE slug = 'outcome'))) THEN
        RETURN;
    END IF;
    PERFORM set_config('aeon.visible_projects', '*', true);
    RETURN QUERY SELECT r.target_node_id FROM node_relations r
        WHERE r.tenant_id = tenant AND r.source_node_id = p_id AND r.type = 'serves'
        ORDER BY r.target_node_id LIMIT 21;
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.visible_projects', coalesce(prior, ''), true);
    RAISE;
END;
$$;

-- Preserve the released work-scope signature and pair/root/cycle bounds.
-- Probe only each recursive parent's indexed children, so a small subtree
-- never materializes the tenant's other trees. Overlapping roots retain every pair,
-- but direct-work-parent discovery probes each distinct node only once.
CREATE OR REPLACE FUNCTION aeon_work_scope(roots uuid[])
RETURNS TABLE(root uuid, id uuid, kind_slug text, fields jsonb, bucket text, is_leaf boolean, project_id uuid)
LANGUAGE plpgsql STABLE AS $$
DECLARE
 scope_roots uuid[];
 scope_nodes uuid[];
BEGIN
 -- Bound the caller's root array before traversal, including duplicate roots.
 IF cardinality(roots)>4096 THEN
  RAISE EXCEPTION 'work scope root budget exceeded' USING ERRCODE='54000';
 END IF;
 -- LIMIT consumes at most budget+1 recursive pairs, before facts/session reads
 -- or the overlapping-root cross product can materialize unbounded work.
 -- UNION still detects cycles; there is no arbitrary depth cutoff.
 WITH RECURSIVE walk(root,id,parent_id,next_page) AS (
  SELECT n.id,n.id,NULL::uuid,false FROM (SELECT DISTINCT r.id FROM unnest(roots) r(id)) seed
  CROSS JOIN LATERAL (SELECT n.id,n.deleted_at FROM nodes n
   WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
    AND n.id=seed.id ORDER BY n.id LIMIT 1) n WHERE n.deleted_at IS NULL
  UNION
  SELECT w.root,c.* FROM walk w CROSS JOIN LATERAL (
   (SELECT c.*,row_number() OVER (ORDER BY c.id)=128 FROM (
    SELECT n.id,n.parent_id FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
     AND n.parent_id=w.id AND n.deleted_at IS NULL AND n.id<>w.root
     AND (n.parent_id,n.id)>=(w.id,'00000000-0000-0000-0000-000000000000'::uuid)
     ORDER BY n.id LIMIT 128
   ) c)
   UNION ALL
   (SELECT c.*,row_number() OVER (ORDER BY c.id)=128 FROM (
    SELECT n.id,n.parent_id FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
     AND w.next_page AND n.parent_id=w.parent_id AND n.deleted_at IS NULL AND n.id<>w.root
     AND (n.parent_id,n.id)>(w.parent_id,w.id) ORDER BY n.id LIMIT 128
   ) c)
  ) c
 ), bounded AS MATERIALIZED (SELECT w.root,w.id FROM walk w LIMIT 50001)
 SELECT array_agg(b.root),array_agg(b.id) INTO scope_roots,scope_nodes FROM bounded b;
 IF cardinality(scope_nodes)>50000 THEN
  RAISE EXCEPTION 'work scope expansion budget exceeded' USING ERRCODE='54000';
 END IF;
 RETURN QUERY
 WITH walk AS MATERIALIZED (
  SELECT p.root,p.id FROM unnest(scope_roots,scope_nodes) AS p(root,id)
 ), facts AS MATERIALIZED (
  SELECT n.id,k.slug,n.fields,n.project_id,n.parent_id,
   CASE aeon_work_status_category(n.state,k.field_schema)
    WHEN 'delivered' THEN 'done' WHEN 'accepted' THEN 'done' WHEN 'blocked' THEN 'open'
    ELSE aeon_work_status_category(n.state,k.field_schema) END AS bucket
  FROM (SELECT DISTINCT w.id FROM walk w) u CROSS JOIN LATERAL (
   SELECT n.id,n.tenant_id,n.kind_id,n.fields,n.state,n.project_id,n.parent_id FROM nodes n WHERE n.id=u.id
    AND n.tenant_id=current_setting('aeon.tenant_id')::uuid OFFSET 0
  ) n CROSS JOIN LATERAL (
   SELECT k.slug,k.field_schema FROM node_kinds k WHERE k.tenant_id=n.tenant_id AND k.id=n.kind_id OFFSET 0
  ) k
 ), work_parents AS MATERIALIZED (
  -- Every visible child of a completed scope is already among these bounded
  -- facts. Derive direct work parents here instead of probing other trees.
  SELECT DISTINCT f.parent_id FROM facts f WHERE f.slug IN ('work','epic','ticket','task')
 ) SELECT w.root,f.id,f.slug,f.fields,f.bucket,
  f.slug IN ('work','epic','ticket','task') AND NOT EXISTS (
   SELECT 1 FROM work_parents p WHERE p.parent_id=f.id),f.project_id
 FROM walk w JOIN facts f ON f.id=w.id;
END;
$$;

-- Keep the invoker visibility walk behind a procedural boundary. Inlining
-- three copies of nodes RLS into the event policy caused multi-second query
-- planning even for a handful of events. No visibility is widened here.
CREATE FUNCTION aeon_goal_event_visible(p_id uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    tenant uuid := NULLIF(current_setting('aeon.tenant_id', true), '')::uuid;
    subject_kind text;
    goal_id uuid;
BEGIN
    SELECT sk.slug, CASE WHEN sk.slug = 'outcome' THEN subject.parent_id ELSE subject.id END
      INTO subject_kind, goal_id
      FROM nodes subject
      JOIN node_kinds sk ON sk.tenant_id = subject.tenant_id AND sk.id = subject.kind_id
     WHERE subject.tenant_id = tenant AND subject.id = p_id;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    RETURN EXISTS (
        SELECT 1 FROM nodes goal
        WHERE goal.tenant_id = tenant AND goal.id = goal_id
          AND goal.kind_id = ANY (ARRAY(SELECT id FROM node_kinds WHERE slug IN ('goal', 'release'))))
      AND EXISTS (
        SELECT 1 FROM nodes outcome
        WHERE outcome.tenant_id = tenant AND outcome.parent_id = goal_id
          AND outcome.kind_id = ANY (ARRAY(SELECT id FROM node_kinds WHERE slug = 'outcome'))
          AND outcome.deleted_at IS NULL
          AND (subject_kind <> 'outcome' OR outcome.id = p_id));
END;
$$;

-- A correlated membership subquery can hash every visible node before checking
-- one event. Check immutable reference identities through point lookups instead,
-- retaining invoker RLS and the prior treatment of empty/null reference arrays.
CREATE FUNCTION aeon_event_refs_visible(refs uuid[]) RETURNS boolean
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    tenant uuid := NULLIF(current_setting('aeon.tenant_id', true), '')::uuid;
    ref uuid;
BEGIN
    FOREACH ref IN ARRAY coalesce(refs, '{}'::uuid[]) LOOP
        IF ref IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM nodes n WHERE n.tenant_id = tenant AND n.id = ref
        ) THEN
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
END;
$$;

-- Keep the existing families, named autopilot types, actor exception and
-- hidden-reference guard. A project reader receives goal events only when
-- that goal has a visible outcome; an outcome event requires that very child.
ALTER POLICY events_project_visibility ON events
    USING ((SELECT aeon_visible_all())
        OR (CASE
                WHEN node_id IS NULL THEN
                    (SELECT aeon_visibility_system())
                    OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[])
                ELSE aeon_event_refs_visible(ARRAY[node_id])
                    AND (split_part(type, '.', 1) IN ('node', 'nodes', 'comment', 'comments', 'attachment',
                            'attachments', 'relation', 'relations', 'import', 'journey', 'intake', 'requirement',
                            'requirements', 'release', 'releases', 'knowledge', 'view', 'views', 'tag', 'tags',
                            'kind', 'kinds', 'profile', 'recurrence')
                         OR type IN ('status_autopilot.proposed', 'status_autopilot.attention_apply', 'status_autopilot.attention_dismiss', 'status_autopilot.attention_undone', 'status_autopilot.changed', 'status_autopilot.undone', 'status_autopilot.skipped', 'status_autopilot.derived', 'status_autopilot.retained', 'status_autopilot.causal_undo')
                         OR (split_part(type, '.', 1) = 'goal' AND aeon_goal_event_visible(node_id))
                         OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[]))
            END
            AND (cardinality(node_refs) = 0
                 OR aeon_event_refs_visible(node_refs))));
