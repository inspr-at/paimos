-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion: only the new admission worker writes these tables.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_admission_outbox (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 sha text NOT NULL,
 context text NOT NULL,
 kind text NOT NULL,
 members jsonb NOT NULL DEFAULT '[]',
 resolved boolean NOT NULL DEFAULT false,
 active boolean NOT NULL DEFAULT true,
 invalidated boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1,
 published_revision bigint NOT NULL DEFAULT 0,
 confirmed_state text NOT NULL DEFAULT '',
 uncertain boolean NOT NULL DEFAULT false,
 checked_at timestamptz,
 PRIMARY KEY(tenant_id,repository,sha,context)
);
CREATE TABLE delivery_admission_queue (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 repository text NOT NULL,
 pull_request bigint NOT NULL,
 head_sha text NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 infra_retries integer NOT NULL DEFAULT 0,
 consumed_group text NOT NULL DEFAULT '',
 policy_removed boolean NOT NULL DEFAULT false,
 quarantined boolean NOT NULL DEFAULT false,
 enforcement text NOT NULL DEFAULT 'pending',
 notice_sent boolean NOT NULL DEFAULT false,
 PRIMARY KEY(tenant_id,repository,pull_request,head_sha)
);
ALTER TABLE delivery_admission_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_admission_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_admission_outbox_tenant ON delivery_admission_outbox
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE delivery_admission_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_admission_queue FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_admission_queue_tenant ON delivery_admission_queue
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Transactional wakeups also cover P1 verdict submission and revocation, which
-- deliberately live outside delivery. No trigger performs network work.
CREATE FUNCTION delivery_admission_dirty() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE fact jsonb; old_fact jsonb; repo text; pr text; project text; invalidate boolean;
BEGIN
 IF TG_OP='DELETE' THEN fact=to_jsonb(OLD); ELSE fact=to_jsonb(NEW); END IF;
 IF TG_OP='UPDATE' THEN
  old_fact=to_jsonb(OLD);
  IF TG_TABLE_NAME='delivery_items' THEN
   -- Publication's own status webhook and observation timestamps are not a
   -- new prerequisite. Otherwise every POST would dirty itself indefinitely.
   fact=jsonb_set(fact-'updated_at','{observation}',
    (fact->'observation'-'at'-'checks') || jsonb_build_object('checks',
     (SELECT coalesce(jsonb_agg(c ORDER BY c->>'name',c),'[]'::jsonb)
      FROM jsonb_array_elements(CASE WHEN jsonb_typeof(fact->'observation'->'checks')='array' THEN fact->'observation'->'checks' ELSE '[]'::jsonb END) c
      WHERE c->>'name'<>'aeon/admission')));
   old_fact=jsonb_set(old_fact-'updated_at','{observation}',
    (old_fact->'observation'-'at'-'checks') || jsonb_build_object('checks',
     (SELECT coalesce(jsonb_agg(c ORDER BY c->>'name',c),'[]'::jsonb)
      FROM jsonb_array_elements(CASE WHEN jsonb_typeof(old_fact->'observation'->'checks')='array' THEN old_fact->'observation'->'checks' ELSE '[]'::jsonb END) c
      WHERE c->>'name'<>'aeon/admission')));
  END IF;
  IF fact=old_fact THEN RETURN NEW; END IF;
 END IF;
 repo=fact->>'repository'; pr=fact->>'pull_request'; project=fact->>'project_id';
 invalidate=TG_TABLE_NAME IN ('delivery_admission_bindings','delivery_admission_approvals','delivery_admission_settings','delivery_admission_queue');
 -- One stable row order for multi-group invalidation.
 PERFORM 1 FROM delivery_admission_outbox o
 WHERE o.tenant_id=(fact->>'tenant_id')::uuid AND (repo IS NULL OR o.repository=repo)
 AND (pr IS NULL OR EXISTS(SELECT 1 FROM jsonb_array_elements(o.members) m WHERE m->>'pr'=pr))
 AND (pr IS NOT NULL OR project IS NULL OR EXISTS(SELECT 1 FROM jsonb_array_elements(o.members) m WHERE m->>'project'=project))
 ORDER BY o.repository,o.sha,o.context FOR UPDATE;
 UPDATE delivery_admission_outbox o SET revision=revision+1,
 invalidated=o.invalidated OR (invalidate AND o.kind='group')
 WHERE o.tenant_id=(fact->>'tenant_id')::uuid AND (repo IS NULL OR o.repository=repo)
 AND (pr IS NULL OR EXISTS(SELECT 1 FROM jsonb_array_elements(o.members) m WHERE m->>'pr'=pr))
 AND (pr IS NOT NULL OR project IS NULL OR EXISTS(SELECT 1 FROM jsonb_array_elements(o.members) m WHERE m->>'project'=project));
 -- Confirmation belongs to the policy observed at the final GitHub action.
 UPDATE delivery_admission_queue q SET enforcement='pending'
 WHERE q.tenant_id=(fact->>'tenant_id')::uuid AND (repo IS NULL OR q.repository=repo)
 AND (pr IS NULL OR q.pull_request::text=pr)
 AND (pr IS NOT NULL OR project IS NULL OR EXISTS(SELECT 1 FROM delivery_items i WHERE i.tenant_id=q.tenant_id AND i.repository=q.repository AND i.pull_request=q.pull_request AND i.project_id::text=project));
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_admission_binding_dirty AFTER INSERT OR UPDATE OR DELETE ON delivery_admission_bindings FOR EACH ROW EXECUTE FUNCTION delivery_admission_dirty();
CREATE TRIGGER delivery_admission_approval_dirty AFTER INSERT OR UPDATE OR DELETE ON delivery_admission_approvals FOR EACH ROW EXECUTE FUNCTION delivery_admission_dirty();
CREATE TRIGGER delivery_admission_policy_dirty AFTER INSERT OR UPDATE OR DELETE ON delivery_admission_settings FOR EACH ROW EXECUTE FUNCTION delivery_admission_dirty();
CREATE TRIGGER delivery_admission_item_dirty AFTER INSERT OR UPDATE OR DELETE ON delivery_items FOR EACH ROW EXECUTE FUNCTION delivery_admission_dirty();
CREATE TRIGGER delivery_admission_ci_policy_dirty AFTER INSERT OR UPDATE OR DELETE ON delivery_settings FOR EACH ROW EXECUTE FUNCTION delivery_admission_dirty();

-- Legacy evidence without a synthetic SHA cannot authorize an automatic retry.
ALTER TABLE delivery_queue_failures ADD COLUMN group_sha text;

CREATE TRIGGER delivery_admission_queue_dirty AFTER UPDATE OF quarantined ON delivery_admission_queue FOR EACH ROW WHEN (OLD.quarantined IS DISTINCT FROM NEW.quarantined) EXECUTE FUNCTION delivery_admission_dirty();
