-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-563. Exact matching only; source-linked/protected workflows stay separate.
CREATE FUNCTION aeon_desk_fingerprint(body jsonb) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT CASE
  WHEN coalesce(body->>'source_request_id','') <> ''
    OR coalesce(body->>'source_handover_id','') <> ''
    OR coalesce(body->>'suggested_outcome','') IN ('requirement','doctrine')
  THEN NULL
  ELSE encode(sha256(convert_to((
    (body - ARRAY['request_id','session_id','source_request_id','source_handover_id','anyway_reason'])
    || jsonb_build_object('suggested_outcome',coalesce(nullif(body->>'suggested_outcome',''),
       CASE WHEN coalesce(body->>'ticket_id','')='' THEN 'always' ELSE 'once' END),
       'options',(SELECT jsonb_agg(o ORDER BY o->>'id') FROM jsonb_array_elements(body->'options') o),
       'blocked_node_ids',coalesce((SELECT jsonb_agg(b ORDER BY b) FROM jsonb_array_elements(body->'blocked_node_ids') b),'[]'::jsonb))
   )::text,'UTF8')),'hex') END
$$;
CREATE INDEX desk_questions_match ON desk_questions(tenant_id,project_id,aeon_desk_fingerprint(input),state,node_id)
 WHERE aeon_desk_fingerprint(input) IS NOT NULL;

-- Immutable provenance belongs to the membership, separate from current answers.
ALTER TABLE desk_askers ADD COLUMN reused_decision_id uuid;
ALTER TABLE desk_askers ADD COLUMN reused_revision bigint;
CREATE UNIQUE INDEX desk_answers_reuse_identity
 ON desk_answers(tenant_id,project_id,question_id,node_id,revision);
ALTER TABLE desk_askers ADD CONSTRAINT desk_askers_reuse_source
 FOREIGN KEY (tenant_id,project_id,question_id,reused_decision_id,reused_revision)
 REFERENCES desk_answers(tenant_id,project_id,question_id,node_id,revision) NOT VALID;
ALTER TABLE desk_askers VALIDATE CONSTRAINT desk_askers_reuse_source;
ALTER TABLE desk_askers ADD CONSTRAINT desk_askers_reuse_pair
 CHECK ((reused_decision_id IS NULL)=(reused_revision IS NULL)) NOT VALID;
ALTER TABLE desk_askers VALIDATE CONSTRAINT desk_askers_reuse_pair;
CREATE FUNCTION aeon_desk_reuse_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.reused_decision_id IS DISTINCT FROM OLD.reused_decision_id
 OR NEW.reused_revision IS DISTINCT FROM OLD.reused_revision THEN
  RAISE EXCEPTION 'asker reuse provenance is immutable' USING ERRCODE='42501';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER desk_reuse_guard BEFORE UPDATE ON desk_askers
 FOR EACH ROW EXECUTE FUNCTION aeon_desk_reuse_guard();
