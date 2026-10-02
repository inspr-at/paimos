-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-562. Node identities remain in the tree. Authority/content lives only in
-- these service-owned projections, never in generic node/knowledge fields.
ALTER TABLE nodes ADD CONSTRAINT nodes_tenant_project_id_unique UNIQUE (tenant_id,project_id,id);
ALTER TABLE inbox_compat_messages ADD CONSTRAINT inbox_compat_project_id_unique UNIQUE (tenant_id,project_id,id);

CREATE TABLE desk_questions (
 tenant_id uuid NOT NULL REFERENCES tenants(id), project_id uuid NOT NULL, node_id uuid NOT NULL,
 input jsonb NOT NULL CHECK (jsonb_typeof(input)='object'),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 state text NOT NULL DEFAULT 'open' CHECK (state IN ('open','answered')),
 suggested_outcome text NOT NULL CHECK (suggested_outcome IN ('once','always','requirement','doctrine')),
 suggestion_reason text NOT NULL CHECK (suggestion_reason IN ('agent_suggestion','ticket_default','project_default')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,node_id), UNIQUE (tenant_id,project_id,node_id),
 FOREIGN KEY (tenant_id,project_id,node_id) REFERENCES nodes(tenant_id,project_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX desk_questions_order ON desk_questions(tenant_id,project_id,created_at,node_id);
CREATE TABLE desk_askers (
 tenant_id uuid NOT NULL REFERENCES tenants(id), project_id uuid NOT NULL, id uuid NOT NULL DEFAULT gen_random_uuid(),
 question_id uuid NOT NULL, principal_id uuid NOT NULL, request_id uuid NOT NULL, request_digest text NOT NULL,
 session_id uuid, source_request_id uuid, source_handover_id uuid, ticket_id uuid, comment_node_id uuid NOT NULL,
 reply_root_id uuid NOT NULL DEFAULT gen_random_uuid(), input jsonb NOT NULL CHECK (jsonb_typeof(input)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id), UNIQUE (tenant_id,project_id,question_id,id),
 UNIQUE (tenant_id,project_id,principal_id,request_id), UNIQUE (tenant_id,reply_root_id),
 FOREIGN KEY (tenant_id,project_id,question_id) REFERENCES desk_questions(tenant_id,project_id,node_id),
 FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,project_id,session_id) REFERENCES harness_sessions(tenant_id,project_id,id),
 FOREIGN KEY (tenant_id,project_id,source_request_id) REFERENCES inbox_compat_messages(tenant_id,project_id,id),
 FOREIGN KEY (tenant_id,project_id,ticket_id) REFERENCES nodes(tenant_id,project_id,id),
 FOREIGN KEY (tenant_id,project_id,comment_node_id) REFERENCES nodes(tenant_id,project_id,id),
 CHECK (source_handover_id IS NULL), -- AEON-524 adapter must verify ownership before enabling
 CHECK (comment_node_id=coalesce(ticket_id,question_id))
);
CREATE INDEX desk_askers_question ON desk_askers(tenant_id,question_id,created_at,id);
CREATE TABLE desk_answers (
 tenant_id uuid NOT NULL REFERENCES tenants(id), project_id uuid NOT NULL, node_id uuid NOT NULL,
 question_id uuid NOT NULL, revision bigint NOT NULL CHECK(revision>1),
 request_id uuid NOT NULL, request_digest text NOT NULL, decided_by uuid NOT NULL,
 option_id text NOT NULL DEFAULT '', answer text NOT NULL CHECK(octet_length(answer) BETWEEN 1 AND 8000), reason text NOT NULL DEFAULT '',
 outcome text NOT NULL CHECK(outcome IN ('once','always','requirement','doctrine')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), deliver_after timestamptz NOT NULL,
 PRIMARY KEY (tenant_id,node_id), UNIQUE (tenant_id,project_id,question_id,revision),
 UNIQUE (tenant_id,question_id,decided_by,request_id),
 FOREIGN KEY (tenant_id,project_id,node_id) REFERENCES nodes(tenant_id,project_id,id),
 FOREIGN KEY (tenant_id,project_id,question_id) REFERENCES desk_questions(tenant_id,project_id,node_id),
 FOREIGN KEY (tenant_id,decided_by) REFERENCES principals(tenant_id,id)
);
-- Separate mutable reuse/effect state from immutable human answer revisions.
CREATE TABLE desk_decisions (
 tenant_id uuid NOT NULL REFERENCES tenants(id), project_id uuid NOT NULL, question_id uuid NOT NULL,
 revision bigint NOT NULL, state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','active','decided','superseded')),
 reuse_count bigint NOT NULL DEFAULT 0 CHECK(reuse_count>=0), superseded_by uuid,
 effect_ref text NOT NULL DEFAULT '',
 PRIMARY KEY (tenant_id,question_id,revision),
 FOREIGN KEY (tenant_id,project_id,question_id,revision) REFERENCES desk_answers(tenant_id,project_id,question_id,revision),
 FOREIGN KEY (tenant_id,superseded_by) REFERENCES desk_answers(tenant_id,node_id)
);
CREATE TABLE desk_pending (
 tenant_id uuid NOT NULL REFERENCES tenants(id), project_id uuid NOT NULL, id uuid NOT NULL DEFAULT gen_random_uuid(),
 question_id uuid NOT NULL, revision bigint NOT NULL, asker_id uuid,
 kind text NOT NULL CHECK(kind IN ('inbox','comment','outcome')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','delivered','failed','replaced')),
 deliver_after timestamptz NOT NULL, effect_ref text NOT NULL DEFAULT '', error_code text NOT NULL DEFAULT '',
 PRIMARY KEY (tenant_id,id), UNIQUE NULLS NOT DISTINCT (tenant_id,question_id,revision,asker_id,kind),
 FOREIGN KEY (tenant_id,project_id,question_id,revision) REFERENCES desk_answers(tenant_id,project_id,question_id,revision),
 FOREIGN KEY (tenant_id,project_id,question_id,asker_id) REFERENCES desk_askers(tenant_id,project_id,question_id,id),
 CHECK ((kind='outcome')=(asker_id IS NULL))
);
CREATE INDEX desk_pending_due ON desk_pending(tenant_id,deliver_after,id) WHERE state='pending';

DO $$ DECLARE tbl text; BEGIN
 FOREACH tbl IN ARRAY ARRAY['desk_questions','desk_askers','desk_answers','desk_decisions','desk_pending'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tbl);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',tbl);
  EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id=NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid)',tbl);
  EXECUTE format('CREATE POLICY project_visibility ON %I AS RESTRICTIVE USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])) WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))',tbl);
 END LOOP;
END $$;

-- Transaction capability set only by the authorized question service. Generic
-- CRUD, bulk writes, kind conversion, imports and undo have no such capability.
CREATE FUNCTION aeon_desk_write_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF coalesce(current_setting('aeon.desk_write',true),'') <> 'on' THEN
  RAISE EXCEPTION 'decision desk records require the question service' USING ERRCODE='42501';
 END IF;
 IF TG_TABLE_NAME='desk_answers' AND TG_OP<>'INSERT' THEN
  RAISE EXCEPTION 'answer revisions are immutable' USING ERRCODE='42501';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE tbl text; BEGIN
 FOREACH tbl IN ARRAY ARRAY['desk_questions','desk_askers','desk_answers','desk_decisions','desk_pending'] LOOP
  EXECUTE format('CREATE TRIGGER desk_write_guard BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION aeon_desk_write_guard()',tbl);
 END LOOP;
END $$;
CREATE FUNCTION aeon_desk_node_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE protected boolean := false;
BEGIN
 IF TG_TABLE_NAME='node_kinds' THEN
  IF TG_OP<>'INSERT' THEN protected := OLD.slug IN ('question','decision'); END IF;
  IF TG_OP<>'DELETE' THEN protected := protected OR NEW.slug IN ('question','decision'); END IF;
 ELSE
  IF TG_OP<>'INSERT' THEN
   SELECT EXISTS(SELECT 1 FROM node_kinds WHERE tenant_id=OLD.tenant_id AND id=OLD.kind_id AND slug IN ('question','decision')) INTO protected;
  END IF;
  IF TG_OP<>'DELETE' THEN
   protected := protected OR EXISTS(SELECT 1 FROM node_kinds WHERE tenant_id=NEW.tenant_id AND id=NEW.kind_id AND slug IN ('question','decision'));
  END IF;
 END IF;
 IF protected AND coalesce(current_setting('aeon.desk_write',true),'') <> 'on' THEN
  RAISE EXCEPTION 'decision desk nodes require the question service' USING ERRCODE='42501';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER desk_node_guard BEFORE INSERT OR UPDATE OR DELETE ON nodes FOR EACH ROW EXECUTE FUNCTION aeon_desk_node_guard();
CREATE TRIGGER desk_kind_guard BEFORE INSERT OR UPDATE OR DELETE ON node_kinds FOR EACH ROW EXECUTE FUNCTION aeon_desk_node_guard();
