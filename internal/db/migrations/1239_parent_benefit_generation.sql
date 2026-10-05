-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-654 integration slot 1239 (renumbered from colliding unpublished 1237). Metadata lives on the already-locked parent,
-- so a derived transition never acquires another lock after the event counter.
-- No historical rows or published release-note snapshots are rewritten.
-- DSAR classification: nodes.benefit_generation is metadata, located by (tenant_id,id).
ALTER TABLE nodes ADD COLUMN benefit_generation jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK (jsonb_typeof(benefit_generation)='object' AND octet_length(benefit_generation::text)<=4096);
CREATE INDEX nodes_parent_benefit_jobs ON nodes(tenant_id,id)
 WHERE deleted_at IS NULL AND benefit_generation->>'status' IN ('queued','running');

CREATE FUNCTION aeon_benefit_texts(fields jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_build_object('pill_en',fields->'pill_en','pill_de',fields->'pill_de',
  'benefit_en',fields->'benefit_en','benefit_de',fields->'benefit_de')
$$;

CREATE FUNCTION aeon_parent_benefit_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE prior_system text := current_setting('aeon.system',true);
 prior_visible text := current_setting('aeon.visible_projects',true); parent boolean;
 prior_category text; next_category text; schema jsonb;
BEGIN
 -- Generation owns only its metadata and four texts. Other writers cancel an
 -- in-flight attempt when a person edits those texts, including edit/revert.
 IF current_setting('aeon.parent_benefit_writer',true)='on' THEN RETURN NEW; END IF;
 IF aeon_benefit_texts(OLD.fields) IS DISTINCT FROM aeon_benefit_texts(NEW.fields)
   AND OLD.benefit_generation<>'{}'::jsonb THEN
  NEW.benefit_generation := OLD.benefit_generation || jsonb_build_object(
    'status','edited','generated',false,'generation',gen_random_uuid()::text,'error','');
  -- An edit saved together with a status change still owns these texts.
  RETURN NEW;
 END IF;
 -- Use canonical shape without exposing hidden child identities to callers.
 PERFORM set_config('aeon.system','on',true),set_config('aeon.visible_projects','*',true);
 SELECT field_schema INTO schema FROM node_kinds WHERE tenant_id=NEW.tenant_id
   AND id=NEW.kind_id AND slug='work';
 IF schema IS NOT NULL AND aeon_work_status_enabled(NEW.project_id) THEN
  SELECT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
    WHERE c.tenant_id=NEW.tenant_id AND c.parent_id=NEW.id AND c.deleted_at IS NULL AND k.slug='work') INTO parent;
  prior_category := aeon_work_status_category(OLD.state,schema);
  next_category := aeon_work_status_category(NEW.state,schema);
  IF parent AND NEW.deleted_at IS NULL AND next_category IN ('done','accepted','delivered')
    AND prior_category NOT IN ('done','accepted','delivered') THEN
   -- Keep an explicitly edited parent and pre-existing authored benefits.
   IF OLD.benefit_generation->>'status'='edited' OR
     (OLD.benefit_generation='{}'::jsonb AND EXISTS(SELECT 1 FROM jsonb_each_text(aeon_benefit_texts(NEW.fields)) t WHERE btrim(coalesce(t.value,''))<>'')) THEN
    NEW.benefit_generation := jsonb_build_object('status','edited','generated',false,'generation',gen_random_uuid()::text);
   ELSE
    NEW.benefit_generation := jsonb_build_object('status','queued','generated',coalesce((OLD.benefit_generation->>'generated')::boolean,false),'generation',gen_random_uuid()::text);
   END IF;
  ELSIF OLD.benefit_generation->>'status' IN ('queued','running','failed') AND
    (NOT parent OR NEW.deleted_at IS NOT NULL OR next_category NOT IN ('done','accepted','delivered')) THEN
   NEW.benefit_generation := OLD.benefit_generation || jsonb_build_object('status','cancelled','generation',gen_random_uuid()::text,'error','');
  END IF;
 END IF;
 PERFORM set_config('aeon.system',coalesce(prior_system,''),true),set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
 RETURN NEW;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('aeon.system',coalesce(prior_system,''),true),set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
 RAISE;
END;
$$;
CREATE TRIGGER z_parent_benefit_transition BEFORE UPDATE OF state,fields,deleted_at ON nodes
 FOR EACH ROW EXECUTE FUNCTION aeon_parent_benefit_transition();
