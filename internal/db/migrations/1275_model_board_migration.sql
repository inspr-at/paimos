-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- The line is independent of version, effort and harness. Match ProfileLine.
CREATE FUNCTION aeon_model_board_line(family text, harness text, model text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
 WITH normalized AS (
  SELECT CASE WHEN harness='pi' THEN regexp_replace(model,'^(anthropic/)?(claude-)?','')
   WHEN harness='claude' THEN regexp_replace(model,'^(claude-)?','')
   WHEN harness='opencode' THEN regexp_replace(model,'^(google/|ollama/)','') ELSE model END AS id
 ), cursor AS (
  SELECT id,CASE WHEN harness='cursor' THEN regexp_match(id,'^(.+?)(-(low|medium|high|xhigh|max|ultra))?(-fast)?$') END AS parts FROM normalized
 ), parts AS (
  SELECT id,parts,regexp_match(id,'^gpt-([0-9]+(?:[.][0-9]+)*)-(.+)$') AS codex,
   regexp_match(CASE WHEN parts IS NULL THEN id ELSE parts[1] END,'^([a-z][a-z-]*?)-?([0-9]+(?:[.-][0-9]+)*)(.*)$') AS concrete FROM cursor
 ) SELECT family||':'||CASE WHEN harness='codex' THEN coalesce(codex[2],model)
   WHEN harness IN ('claude','pi') AND id IN ('opus','sonnet','haiku','fable') THEN id
   WHEN concrete IS NOT NULL THEN concrete[1]||concrete[3]||coalesce(parts[4],'') ELSE model END FROM parts;
$$;
-- Retain full old snapshots and every nonrepresentable decision in the audit.
-- No source row is changed or deleted. Migration is idempotent per tenant.
CREATE FUNCTION aeon_migrate_model_board(p_tenant uuid) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.tenant_id',true); visible text := current_setting('aeon.visible_projects',true);
 audit jsonb; actor uuid; scope_row record; cell_row record; profile uuid; chosen_line text; word text; col text; columns text[];
 balanced text[]; snapshot_size bigint; baseline text[] := ARRAY['openai:sol','anthropic:sonnet','anthropic:opus','openai:astra','anthropic:fable']; ranked text[];
BEGIN
 PERFORM set_config('aeon.tenant_id',p_tenant::text,true);
 PERFORM set_config('aeon.visible_projects','*',true);
 SELECT m.audit INTO audit FROM model_pref_migrations m WHERE m.tenant_id=p_tenant;
 IF audit IS NOT NULL THEN
  PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
  PERFORM set_config('aeon.visible_projects',coalesce(visible,''),true);
  RETURN audit;
 END IF;
 -- Bound the complete retained snapshot before aggregation or conversion.
 SELECT coalesce((SELECT sum(octet_length(to_jsonb(s)::text)) FROM model_pref_scopes s),0)
  +coalesce((SELECT sum(octet_length(to_jsonb(r)::text)) FROM model_pref_rows r),0)
  +coalesce((SELECT sum(octet_length(to_jsonb(c)::text))*2 FROM model_pref_cells c),0) INTO snapshot_size;
 IF snapshot_size>786432 THEN RAISE EXCEPTION 'model preference migration snapshot exceeds audit bound'; END IF;
 SELECT jsonb_build_object('scopes',coalesce((SELECT jsonb_agg(to_jsonb(s)) FROM model_pref_scopes s),'[]'::jsonb),
  'rows',coalesce((SELECT jsonb_agg(to_jsonb(r)) FROM model_pref_rows r),'[]'::jsonb),
  'cells',coalesce((SELECT jsonb_agg(to_jsonb(c)) FROM model_pref_cells c),'[]'::jsonb),
  'dropped',coalesce((SELECT jsonb_agg(jsonb_build_object('scope',c.scope_id,'kind',c.kind_id,'bucket',c.bucket,'mode',c.mode,'locked',r.locked))
   FROM model_pref_cells c JOIN model_pref_rows r USING(tenant_id,scope_id,kind_id)
   WHERE c.mode='auto' OR r.locked OR (c.bucket='complex' AND EXISTS(SELECT 1 FROM model_pref_cells n WHERE n.scope_id=c.scope_id AND n.kind_id=c.kind_id AND n.bucket='normal'))),'[]'::jsonb),
  'needs_you',jsonb_build_array('check_work_kind_sentences')) INTO audit;
 SELECT id INTO actor FROM principals WHERE kind='agent' AND name='Model preference migration' LIMIT 1;
 IF actor IS NULL THEN INSERT INTO principals(tenant_id,kind,name) VALUES(p_tenant,'agent','Model preference migration') RETURNING id INTO actor; END IF;
 INSERT INTO model_pref_profiles(tenant_id,scope,template,thinking,usage,set_by) VALUES(p_tenant,'workspace','balanced','standard','balanced',actor);
 FOR scope_row IN SELECT * FROM model_pref_scopes ORDER BY level,id LOOP
  profile := NULL;
  IF scope_row.level<>'project' THEN
   IF scope_row.level='person' THEN
   INSERT INTO model_pref_profiles(tenant_id,scope,person_id,residency,set_by)
    VALUES(p_tenant,CASE scope_row.level WHEN 'default' THEN 'workspace' ELSE 'person' END,scope_row.person_id,scope_row.residency,scope_row.updated_by);
   END IF;
   SELECT id INTO profile FROM model_pref_profiles WHERE scope=CASE scope_row.level WHEN 'default' THEN 'workspace' ELSE 'person' END
    AND person_id IS NOT DISTINCT FROM scope_row.person_id;
   UPDATE model_pref_profiles SET residency=scope_row.residency WHERE id=profile;
  END IF;
  FOR cell_row IN SELECT DISTINCT ON (c.kind_id) c.*,k.slug,p.model,p.effort AS pinned_effort,
    aeon_model_board_line(p.family,p.harness,p.model) AS pinned_line,coalesce(d.effort_level,aeon_model_effort_level(c.family,c.effort)) AS effort_level
    FROM model_pref_cells c JOIN work_kinds k ON k.id=c.kind_id AND k.tenant_id=c.tenant_id
    LEFT JOIN model_profiles p ON p.id=c.profile_id AND p.tenant_id=c.tenant_id
    LEFT JOIN model_profile_display d ON d.profile_id=p.id AND d.tenant_id=p.tenant_id
    WHERE c.scope_id=scope_row.id ORDER BY c.kind_id,(c.bucket='normal') DESC LOOP
   IF cell_row.mode='auto' THEN CONTINUE; END IF;
   chosen_line := CASE WHEN cell_row.mode='pinned' THEN cell_row.pinned_line ELSE cell_row.family||':'||cell_row.line END;
   IF chosen_line IS NULL THEN RAISE EXCEPTION 'model preference migration lost a line'; END IF;
   word := CASE WHEN cell_row.effort_level<=2 THEN 'lean' WHEN cell_row.effort_level=4 THEN 'deep' WHEN cell_row.effort_level=5 THEN 'max' ELSE 'standard' END;
   columns := CASE WHEN cell_row.slug='review' THEN ARRAY['review:openai','review:anthropic','review:xai'] ELSE ARRAY[cell_row.slug] END;
   FOREACH col IN ARRAY columns LOOP
    IF scope_row.level='project' THEN
     INSERT INTO model_rules(tenant_id,scope,project_id,column_key,line,lock,why,set_by)
      VALUES(p_tenant,'project',scope_row.project_id,col,chosen_line,'top',
       CASE WHEN col='frontend' AND chosen_line='anthropic:sonnet' THEN 'Was AEON’s pinned UI build model (Sonnet 5.5 xhigh)'
        ELSE 'Migrated project model preference ('||coalesce(cell_row.model,chosen_line)||' '||coalesce(cell_row.pinned_effort,cell_row.effort,'')||')' END,actor);
     INSERT INTO model_rule_revisions(tenant_id,scope,scope_key) SELECT p_tenant,'project',scope_row.project_id::text WHERE NOT EXISTS(SELECT 1 FROM model_rule_revisions WHERE scope='project' AND scope_key=scope_row.project_id::text);
    ELSE
     balanced := CASE WHEN col LIKE 'review:%' THEN ARRAY['openai:sol','xai:grok','anthropic:opus','anthropic:fable']
      WHEN col='design' THEN ARRAY['anthropic:opus','anthropic:sonnet','openai:sol','anthropic:fable','openai:astra'] ELSE baseline END;
     ranked := ARRAY[chosen_line]||array_remove(balanced,chosen_line);
     INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,thinking,set_by,set_at)
      VALUES(p_tenant,profile,col,'first',CASE WHEN scope_row.level='default' AND ranked=balanced THEN NULL ELSE ranked END,word,scope_row.updated_by,scope_row.updated_at)
     ;
    END IF;
   END LOOP;
  END LOOP;
 END LOOP;
 audit := audit||jsonb_build_object('migration_actor',actor);
 INSERT INTO model_pref_migrations(tenant_id,audit) VALUES(p_tenant,audit);
 PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
 PERFORM set_config('aeon.visible_projects',coalesce(visible,''),true);
 RETURN audit;
END;
$$;
CREATE FUNCTION aeon_migrate_all_model_boards() RETURNS SETOF model_pref_migrations LANGUAGE plpgsql AS $$
DECLARE t record; prior text := current_setting('aeon.tenant_id',true); result jsonb; migrated uuid[] := '{}'; tenant_key uuid;
BEGIN
 PERFORM id FROM tenants ORDER BY id FOR NO KEY UPDATE;
 FOR t IN SELECT id FROM tenants ORDER BY id LOOP
  PERFORM set_config('aeon.tenant_id',t.id::text,true);
  IF EXISTS(SELECT 1 FROM model_pref_migrations WHERE tenant_id=t.id) THEN CONTINUE; END IF;
  result:=aeon_migrate_model_board(t.id);
  migrated:=array_append(migrated,t.id);
 END LOOP;
 -- All resource writes and locks precede every event counter.
 FOREACH tenant_key IN ARRAY migrated LOOP
  PERFORM set_config('aeon.tenant_id',tenant_key::text,true);
  SELECT audit INTO result FROM model_pref_migrations WHERE tenant_id=tenant_key;
  INSERT INTO events(tenant_id,actor_principal_id,type,after)
   VALUES(tenant_key,(result->>'migration_actor')::uuid,'model.preferences_migrated',result);
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
 RETURN;
END;
$$;
-- Invoke the bootstrap with an expand-safe statement. It emits no rows;
-- receipt writes occur before the final event flush inside the function.
INSERT INTO model_pref_migrations(tenant_id,audit) SELECT tenant_id,audit FROM aeon_migrate_all_model_boards();
