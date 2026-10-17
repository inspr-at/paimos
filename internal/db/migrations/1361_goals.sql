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

-- Ordinary rows must stay index lookups. NOT IN (SELECT …) is planned as an
-- anti-join, and with the volatile serves walk beside it every node read
-- becomes a sequential rescan once statistics are missing. The kind catalog
-- is one scalar InitPlan. CASE does not call the serves walk unless the row's
-- kind is an outcome.
CREATE POLICY nodes_outcome_ticket_visibility ON nodes AS RESTRICTIVE
    USING (CASE
        WHEN (SELECT aeon_visible_all()) THEN true
        WHEN kind_id <> ALL (COALESCE((SELECT array_agg(id) FROM node_kinds WHERE slug = 'outcome'), '{}'::uuid[])) THEN true
        ELSE aeon_outcome_tickets_visible(id)
    END)
    WITH CHECK (CASE
        WHEN (SELECT aeon_visible_all()) THEN true
        WHEN kind_id <> ALL (COALESCE((SELECT array_agg(id) FROM node_kinds WHERE slug = 'outcome'), '{}'::uuid[])) THEN true
        ELSE aeon_outcome_tickets_visible(id)
    END);

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
