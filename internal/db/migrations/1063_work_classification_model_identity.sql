-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-503 B: additive classification schema and auditable session identity.
SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_sessions ADD COLUMN model_profile_id uuid,
    ADD COLUMN model_raw text,
    ADD CONSTRAINT harness_session_model_profile_fk FOREIGN KEY (tenant_id, model_profile_id)
        REFERENCES model_profiles(tenant_id, id);

CREATE FUNCTION aeon_work_classification_properties() RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
SELECT jsonb_build_object(
 'route_role_source', jsonb_build_object('type',jsonb_build_array('string','null'),'enum',jsonb_build_array('agent','person','suggested',NULL)),
 'area_source', jsonb_build_object('type',jsonb_build_array('string','null'),'enum',jsonb_build_array('agent','person','suggested',NULL)),
 'route_role_confirmed', jsonb_build_object('type','boolean','description','Server-written; only a person confirms classifications.'),
 'area_confirmed', jsonb_build_object('type','boolean','description','Server-written; only a person confirms classifications.'),
 'complexity', jsonb_build_object('type',jsonb_build_array('string','null'),'enum',jsonb_build_array('S','M','L',NULL),'description','Suggested from hours: <=2 S, <=8 M, >8 L; build-hard promotes one bucket.'),
 'complexity_source', jsonb_build_object('type',jsonb_build_array('string','null'),'enum',jsonb_build_array('agent','person','suggested',NULL)),
 'complexity_by', jsonb_build_object('type',jsonb_build_array('string','null'),'description','Server-written acting principal id.'),
 'complexity_at', jsonb_build_object('type',jsonb_build_array('string','null'),'description','Server-written UTC timestamp.'),
 'complexity_confirmed', jsonb_build_object('type','boolean','description','Server-written; only a person confirms classifications.')
);
$$;

-- Keep the published starter-kind function intact. This extension also covers
-- future tenant seeds and custom strict work schemas without rewriting nodes.
CREATE FUNCTION aeon_extend_work_classification_schema() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.slug IN ('ticket','task') THEN
  NEW.field_schema := jsonb_set(NEW.field_schema, '{properties}',
   coalesce(NEW.field_schema->'properties','{}'::jsonb) || aeon_work_classification_properties());
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER work_classification_schema BEFORE INSERT OR UPDATE ON node_kinds
 FOR EACH ROW EXECUTE FUNCTION aeon_extend_work_classification_schema();

-- The same normalizer drives registration, heartbeat identity and backfill.
-- Only recognized trailing effort tokens are separated. Conflicting explicit
-- effort is retained but cannot resolve a profile; neither can an absent effort.
CREATE FUNCTION aeon_session_model_key(p_model text, p_effort text)
RETURNS TABLE(model text, effort text, consistent boolean)
LANGUAGE sql IMMUTABLE AS $$
 WITH input AS (
  SELECT nullif(btrim(p_model),'') AS raw, nullif(btrim(p_effort),'') AS supplied
 ), suffix AS (
  SELECT *, substring(raw FROM '^.+-(low|medium|high|xhigh|max|ultra)$') AS embedded FROM input
 )
 SELECT CASE WHEN embedded IS NULL THEN raw ELSE left(raw,length(raw)-length(embedded)-1) END,
  CASE WHEN embedded IS NOT NULL AND (supplied IS NULL OR supplied='default') THEN embedded ELSE supplied END,
  embedded IS NULL OR supplied IS NULL OR supplied='default' OR supplied=embedded
 FROM suffix;
$$;

CREATE FUNCTION aeon_harness_model_identity(p_harness text, p_model text, p_effort text)
RETURNS TABLE(model text, effort text, profile_id uuid)
LANGUAGE sql STABLE AS $$
 WITH key AS (SELECT * FROM aeon_session_model_key(p_model,p_effort)), matches AS (
  SELECT p.id FROM model_profiles p CROSS JOIN key k
   CROSS JOIN LATERAL aeon_session_model_key(p.model,p.effort) pk
  WHERE p.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
   AND p.harness=p_harness AND k.consistent AND pk.consistent
   AND k.model=pk.model AND k.effort=pk.effort
 )
 SELECT k.model,k.effort, CASE WHEN (SELECT count(*) FROM matches)=1
  THEN (SELECT id FROM matches LIMIT 1) ELSE NULL END FROM key k;
$$;

-- A tenant-scoped, idempotent maintenance operation. Only unique matches are
-- rewritten; raw metadata and registration replay digests remain auditable.
CREATE FUNCTION aeon_backfill_session_model_profiles() RETURNS bigint
LANGUAGE sql VOLATILE AS $$
 WITH candidates AS (
  SELECT s.id,i.model,i.effort,i.profile_id FROM harness_sessions s
   CROSS JOIN LATERAL aeon_harness_model_identity(s.harness,s.model,s.reasoning_effort) i
  WHERE s.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
   AND s.model_profile_id IS NULL AND i.profile_id IS NOT NULL
 ), updated AS (
  UPDATE harness_sessions s SET model_raw=coalesce(s.model_raw,s.model),
   model=c.model,reasoning_effort=c.effort,model_profile_id=c.profile_id
  FROM candidates c WHERE s.id=c.id
   AND s.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
   AND s.model_profile_id IS NULL RETURNING s.id
 ) SELECT count(*) FROM updated;
$$;
