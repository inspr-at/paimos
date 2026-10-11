-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Retain legacy writers and their source rows for the deprecation release.
ALTER TABLE model_pref_orders ADD COLUMN end_mode text, ADD COLUMN efforts jsonb;
ALTER TABLE model_rules ADD COLUMN moved_to_models boolean;

-- Separate situations avoid changing the released order table's CHECK.
CREATE TABLE model_pref_situation_orders (
 tenant_id uuid NOT NULL REFERENCES tenants(id), profile_id uuid NOT NULL,
 column_key text NOT NULL, situation text NOT NULL CHECK (situation IN ('small','review')),
 rank text[], not_allowed text[] NOT NULL DEFAULT '{}', thinking text,
 effort text, effort_level smallint, end_mode text, efforts jsonb,
 set_by uuid, set_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,profile_id,column_key,situation),
 FOREIGN KEY (tenant_id,profile_id) REFERENCES model_pref_profiles(tenant_id,id)
);
CREATE TABLE model_pauses (
 tenant_id uuid NOT NULL REFERENCES tenants(id), harness text NOT NULL, model text NOT NULL,
 until_at timestamptz, reason text NOT NULL, set_by uuid, set_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,harness,model)
);
-- Chain-tail and never entries retain workspace/project ownership and stamps.
CREATE TABLE model_row_constraints (
 tenant_id uuid NOT NULL REFERENCES tenants(id), scope text NOT NULL, project_id uuid,
 column_key text NOT NULL, line text NOT NULL, kind text NOT NULL CHECK (kind IN ('chain','never')),
 position integer NOT NULL DEFAULT 0, why text NOT NULL, set_by uuid,
 set_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX model_row_constraints_scope ON model_row_constraints
 (tenant_id,scope,coalesce(project_id,'00000000-0000-0000-0000-000000000000'),column_key,line);
CREATE TABLE model_v2_migrations (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id), audit jsonb NOT NULL,
 migrated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE model_pref_situation_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pref_situation_orders FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pref_situation_orders_tenant ON model_pref_situation_orders
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_pauses ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_pauses FORCE ROW LEVEL SECURITY;
CREATE POLICY model_pauses_tenant ON model_pauses
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_row_constraints ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_row_constraints FORCE ROW LEVEL SECURITY;
CREATE POLICY model_row_constraints_tenant ON model_row_constraints
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE model_v2_migrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_v2_migrations FORCE ROW LEVEL SECURITY;
CREATE POLICY model_v2_migrations_tenant ON model_v2_migrations
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Match Models' shared provider identity across harnesses (S1 modelsLine).
CREATE FUNCTION aeon_models_line(family text, harness text, model text) RETURNS text
 LANGUAGE sql IMMUTABLE AS $$
 SELECT aeon_model_board_line(family,
  CASE WHEN family='anthropic' THEN 'claude' WHEN family='openai' AND model LIKE 'gpt-%' THEN 'codex' ELSE harness END,
  CASE WHEN family='anthropic' THEN regexp_replace(model,'^anthropic/','') ELSE model END);
$$;

-- Stored picker aliases are normalized only when the registry knows them.
CREATE FUNCTION aeon_models_canonical_line(line text) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT aeon_models_line(p.family,p.harness,p.model) FROM model_profiles p
  WHERE aeon_model_board_line(p.family,p.harness,p.model)=line ORDER BY p.slug,p.version,p.id LIMIT 1),line);
$$;

CREATE FUNCTION aeon_migrate_models_v2(p_tenant uuid) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.tenant_id',true); visible text := current_setting('aeon.visible_projects',true);
 audit jsonb; actor uuid; workspace uuid; item record; target record; source_rows jsonb; moved jsonb := '[]';
 ranked text[]; native jsonb; pick text; existing text[]; bytes bigint; stamp timestamptz := now();
BEGIN
 PERFORM set_config('aeon.tenant_id',p_tenant::text,true);
 PERFORM set_config('aeon.visible_projects','*',true);
 PERFORM id FROM tenants WHERE id=p_tenant FOR NO KEY UPDATE;
 SELECT m.audit INTO audit FROM model_v2_migrations m WHERE m.tenant_id=p_tenant;
 IF audit IS NOT NULL THEN
  PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
  PERFORM set_config('aeon.visible_projects',coalesce(visible,''),true);
  RETURN audit;
 END IF;
 -- Bounds precede JSON aggregation and every data write. Oversize aborts;
 -- a partially migrated tenant must never be reported as migrated.
 SELECT coalesce((SELECT sum(octet_length(to_jsonb(r)::text)) FROM model_role_routes r),0)
  +coalesce((SELECT sum(octet_length(to_jsonb(r)::text)) FROM model_security_role_routes r),0)
  +coalesce((SELECT sum(octet_length(to_jsonb(r)::text)) FROM model_rules r),0)
  +coalesce((SELECT sum(octet_length(to_jsonb(o)::text)) FROM model_pref_orders o),0)
  +coalesce((SELECT sum(octet_length(pref.value::text)) FROM user_preferences pref WHERE pref.key='agents.working'),0) INTO bytes;
 IF bytes>262144 THEN RAISE EXCEPTION 'Models v2 migration exceeds audit bound'; END IF;
 SELECT jsonb_build_object(
  'ladders',coalesce((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.role,r.priority) FROM model_role_routes r),'[]'::jsonb),
  'security_ladder',coalesce((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.priority) FROM model_security_role_routes r),'[]'::jsonb),
  'orders',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.profile_id,o.column_key,o.situation) FROM model_pref_orders o),'[]'::jsonb),
  'rules',coalesce((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.scope,r.column_key,r.position,r.line) FROM model_rules r),'[]'::jsonb),
  'dial',coalesce((SELECT jsonb_agg(jsonb_build_object('person',pref.principal_id,'value',pref.value) ORDER BY pref.principal_id) FROM user_preferences pref WHERE pref.key='agents.working'),'[]'::jsonb)) INTO source_rows;
 SELECT id INTO actor FROM principals WHERE kind='agent' AND name='Models v2 migration' ORDER BY id LIMIT 1;
 IF actor IS NULL THEN INSERT INTO principals(tenant_id,kind,name) VALUES(p_tenant,'agent','Models v2 migration') RETURNING id INTO actor; END IF;
 IF NOT EXISTS(SELECT 1 FROM model_pref_profiles WHERE scope='workspace') THEN
 INSERT INTO model_pref_profiles(tenant_id,scope,template,thinking,usage,set_by)
 VALUES(p_tenant,'workspace','balanced','standard','balanced',actor); END IF;
 SELECT id INTO workspace FROM model_pref_profiles WHERE scope='workspace';
 FOR item IN SELECT role FROM (VALUES('build'),('build-hard'),('review-gate')) roles(role) WHERE EXISTS(SELECT 1 FROM model_profiles) LOOP
  IF item.role='review-gate' AND coalesce((SELECT ordinary_review_order_mode FROM model_review_order),'legacy')<>'saved' THEN CONTINUE; END IF;
  SELECT array_agg(line ORDER BY priority),jsonb_object_agg(line,effort ORDER BY priority DESC) INTO ranked,native
  FROM (SELECT DISTINCT ON (aeon_models_line(p.family,p.harness,p.model))
   aeon_models_line(p.family,p.harness,p.model) AS line,r.priority,p.effort
   FROM model_role_routes r JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id
   WHERE r.role=item.role ORDER BY aeon_models_line(p.family,p.harness,p.model),r.priority) lines;
  ranked := coalesce(ranked,'{}'); native := coalesce(native,'{}');
  IF item.role='build' THEN
   SELECT rank INTO existing FROM model_pref_orders WHERE profile_id=workspace AND column_key='other' AND situation='first';
   -- A saved multi-step Default already has its own chain.
   IF cardinality(existing)>1 THEN CONTINUE; END IF;
   pick := existing[1];
   IF pick IS NOT NULL THEN ranked := ARRAY[pick]||array_remove(ranked,aeon_models_canonical_line(pick)); native:=native-aeon_models_canonical_line(pick); END IF;
   IF EXISTS(SELECT 1 FROM model_pref_orders WHERE profile_id=workspace AND column_key='other' AND situation='first') THEN
    UPDATE model_pref_orders SET rank=ranked,efforts=native||coalesce(efforts,'{}'),end_mode=coalesce(end_mode,'wait') WHERE profile_id=workspace AND column_key='other' AND situation='first';
   ELSE INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,efforts,end_mode,set_by)
    VALUES(p_tenant,workspace,'other','first',ranked,native,'wait',actor); END IF;
   moved := moved||jsonb_build_array(jsonb_build_object('from','build','to','other/first','rank',ranked,'efforts',native));
  ELSIF item.role='build-hard' THEN
   IF EXISTS(SELECT 1 FROM model_pref_orders WHERE profile_id=workspace AND column_key='other' AND situation='stuck' AND rank IS NOT NULL) THEN CONTINUE; END IF;
   IF EXISTS(SELECT 1 FROM model_pref_orders WHERE profile_id=workspace AND column_key='other' AND situation='stuck') THEN
    UPDATE model_pref_orders SET rank=ranked,efforts=native,end_mode='row' WHERE profile_id=workspace AND column_key='other' AND situation='stuck';
   ELSE INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,efforts,end_mode,set_by)
    VALUES(p_tenant,workspace,'other','stuck',ranked,native,'row',actor); END IF;
   moved := moved||jsonb_build_array(jsonb_build_object('from','build-hard','to','other/stuck','rank',ranked,'efforts',native));
  ELSE
   INSERT INTO model_pref_situation_orders(tenant_id,profile_id,column_key,situation,rank,efforts,end_mode,set_by)
   VALUES(p_tenant,workspace,'other','review',ranked,native,'wait',actor);
   moved := moved||jsonb_build_array(jsonb_build_object('from','review-gate','to','other/review','rank',ranked,'efforts',native));
  END IF;
 END LOOP;
 INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by)
 SELECT DISTINCT ON (p.harness,p.model) p_tenant,p.harness,p.model,r.valid_until,r.reason,actor
 FROM (SELECT * FROM model_role_routes UNION ALL SELECT * FROM model_security_role_routes) r
 JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id
 WHERE r.state<>'available' AND r.valid_until>stamp
 ORDER BY p.harness,p.model,r.valid_until DESC,r.role,r.priority;
 FOR item IN SELECT r.*,p.harness,p.model FROM (SELECT * FROM model_role_routes UNION ALL SELECT * FROM model_security_role_routes) r
  JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id WHERE r.state<>'available' AND r.valid_until>stamp ORDER BY r.role,r.priority LOOP
  moved := moved||jsonb_build_array(jsonb_build_object('from',to_jsonb(item),'to',jsonb_build_object('pause',item.harness||'/'||item.model)));
 END LOOP;
 INSERT INTO model_row_constraints(tenant_id,scope,project_id,column_key,line,kind,position,why,set_by,set_at)
 SELECT DISTINCT ON (scope,project_id,column_key,aeon_models_canonical_line(line)) tenant_id,scope,project_id,column_key,aeon_models_canonical_line(line),
  CASE lock WHEN 'bottom' THEN 'chain' ELSE 'never' END,position,why,set_by,set_at FROM model_rules WHERE lock IN ('bottom','not')
 ORDER BY scope,project_id,column_key,aeon_models_canonical_line(line),(lock='not') DESC,position,line;
 -- Sources retain every stamp and value for older readers; Models reads the
 -- copied constraints, appending chain entries after the row's order.
 FOR item IN SELECT * FROM model_rules WHERE lock IN ('bottom','not') ORDER BY scope,column_key,position,line LOOP
  moved := moved||jsonb_build_array(jsonb_build_object('from',to_jsonb(item),'to',CASE item.lock WHEN 'bottom' THEN 'chain' ELSE 'never' END));
 END LOOP;
 UPDATE model_rules SET moved_to_models=true WHERE lock IN ('bottom','not');
 -- Saved Wait belongs to the person's rows. Canonical linked ownership is
 -- retained; do not impose one person's dial on the whole workspace.
 FOR item IN SELECT coalesce(p.linked_to,p.id) AS person,pref.value,entry.key AS harness
  FROM user_preferences pref JOIN principals p ON p.tenant_id=pref.tenant_id AND p.id=pref.principal_id
  CROSS JOIN LATERAL jsonb_each(coalesce(pref.value->'daily','{}')) entry
  WHERE pref.key='agents.working' AND p.kind='person' AND p.status='active' AND entry.value->>'at_limit'='wait'
  ORDER BY person,entry.key LOOP
  IF NOT EXISTS(SELECT 1 FROM model_pref_profiles WHERE scope='person' AND person_id=item.person) THEN
   INSERT INTO model_pref_profiles(tenant_id,scope,person_id,set_by) VALUES(p_tenant,'person',item.person,actor); END IF;
  FOR target IN SELECT own.id AS profile_id,col.column_key,picked.line,picked.effort,picked.effort_level,picked.efforts
  FROM model_pref_profiles own CROSS JOIN (SELECT DISTINCT column_key FROM model_pref_orders WHERE situation='first' UNION SELECT slug FROM work_kinds WHERE archived_at IS NULL UNION SELECT 'other') col
  CROSS JOIN LATERAL (
   SELECT o.rank[1] AS line,o.effort,o.effort_level,o.efforts FROM model_pref_orders o JOIN model_pref_profiles owner ON owner.id=o.profile_id
   WHERE (owner.id=own.id OR owner.id=workspace) AND o.column_key IN (col.column_key,'other') AND o.situation='first' AND o.rank IS NOT NULL
   ORDER BY (owner.id=own.id) DESC,(o.column_key=col.column_key) DESC LIMIT 1) picked
  WHERE own.scope='person' AND own.person_id=item.person AND EXISTS(SELECT 1 FROM model_profiles m WHERE aeon_models_line(m.family,m.harness,m.model)=aeon_models_canonical_line(picked.line) AND m.harness=item.harness) LOOP
   IF EXISTS(SELECT 1 FROM model_pref_orders WHERE profile_id=target.profile_id AND column_key=target.column_key AND situation='first') THEN
    UPDATE model_pref_orders SET rank=ARRAY[target.line],end_mode='wait' WHERE profile_id=target.profile_id AND column_key=target.column_key AND situation='first';
   ELSE INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,end_mode,effort,effort_level,efforts,set_by)
    VALUES(p_tenant,target.profile_id,target.column_key,'first',ARRAY[target.line],'wait',target.effort,target.effort_level,target.efforts,actor); END IF;
  END LOOP;
  moved := moved||jsonb_build_array(jsonb_build_object('from','dial/wait','person',item.person,'harness',item.harness,'to',
   coalesce((SELECT jsonb_agg(jsonb_build_object('profile',o.profile_id,'column',o.column_key,'rank',o.rank,'end',o.end_mode) ORDER BY o.column_key)
   FROM model_pref_orders o JOIN model_pref_profiles own ON own.id=o.profile_id WHERE own.person_id=item.person AND o.situation='first' AND o.end_mode='wait'),'[]'::jsonb)));
 END LOOP;
 UPDATE model_pref_profiles SET revision=revision+1 WHERE scope='workspace' OR EXISTS(SELECT 1 FROM model_pref_orders o WHERE o.profile_id=model_pref_profiles.id);
 audit := jsonb_build_object('migration',1333,'migration_actor',actor,'before',source_rows,'moved',moved,
  'orders',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.profile_id,o.column_key,o.situation) FROM model_pref_orders o),'[]'::jsonb),
  'situations',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.profile_id,o.column_key,o.situation) FROM model_pref_situation_orders o),'[]'::jsonb),
  'pauses',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.harness,o.model) FROM model_pauses o),'[]'::jsonb),
  'constraints',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.scope,o.column_key,o.position,o.line) FROM model_row_constraints o),'[]'::jsonb));
 IF octet_length(audit::text)>1048576 THEN RAISE EXCEPTION 'Models v2 migration event exceeds audit bound'; END IF;
 INSERT INTO model_v2_migrations(tenant_id,audit) VALUES(p_tenant,audit);
 PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
 PERFORM set_config('aeon.visible_projects',coalesce(visible,''),true);
 RETURN audit;
END;
$$;

CREATE FUNCTION aeon_migrate_all_models_v2() RETURNS SETOF model_v2_migrations LANGUAGE plpgsql AS $$
DECLARE t record; prior text := current_setting('aeon.tenant_id',true); result jsonb; migrated uuid[] := '{}'; tenant_key uuid;
BEGIN
 PERFORM id FROM tenants ORDER BY id FOR NO KEY UPDATE;
 FOR t IN SELECT id FROM tenants ORDER BY id LOOP
  PERFORM set_config('aeon.tenant_id',t.id::text,true);
  IF EXISTS(SELECT 1 FROM model_v2_migrations WHERE tenant_id=t.id) THEN CONTINUE; END IF;
  result:=aeon_migrate_models_v2(t.id); migrated:=array_append(migrated,t.id);
 END LOOP;
 -- No resource writes or locks after the first tenant's event counter.
 FOREACH tenant_key IN ARRAY migrated LOOP
  PERFORM set_config('aeon.tenant_id',tenant_key::text,true);
  SELECT audit INTO result FROM model_v2_migrations WHERE tenant_id=tenant_key;
  INSERT INTO events(tenant_id,actor_principal_id,type,after)
   VALUES(tenant_key,(result->>'migration_actor')::uuid,'model.settings_migrated',result);
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
 RETURN;
END;
$$;
INSERT INTO model_v2_migrations(tenant_id,audit) SELECT tenant_id,audit FROM aeon_migrate_all_models_v2();
