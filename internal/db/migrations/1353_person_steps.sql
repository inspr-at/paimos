-- SPDX-License-Identifier: AGPL-3.0-only
-- Requests are projections of nodes/definitions and their append-only events.
-- No second queue owns completion or assignment. INVOKER functions retain RLS.
CREATE FUNCTION aeon_person_step_owner(p_project uuid, p_preferred uuid, p_fallback uuid)
RETURNS uuid LANGUAGE sql STABLE AS $$
 SELECT p.id FROM principals p
 WHERE p.tenant_id=nullif(current_setting('aeon.tenant_id',true),'')::uuid
 AND p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 AND EXISTS(SELECT 1 FROM role_bindings b
   WHERE b.tenant_id=p.tenant_id AND b.principal_id=p.id
   AND (b.scope_type='workspace' OR b.scope_type='project' AND b.scope_id=p_project)
   AND aeon_role_reads_nodes(p.tenant_id,b.role_id,b.scope_type))
 AND (p.id IN (p_preferred,p_fallback) OR EXISTS(
   SELECT 1 FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
   WHERE b.tenant_id=p.tenant_id AND b.principal_id=p.id
   AND (b.scope_type='project' AND b.scope_id=p_project AND r.key='admin'
     OR b.scope_type='workspace' AND r.key IN ('owner','admin'))))
 ORDER BY CASE WHEN p.id=p_preferred THEN 0 WHEN p.id=p_fallback THEN 1 ELSE 2 END,
   CASE WHEN EXISTS(SELECT 1 FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
    WHERE b.tenant_id=p.tenant_id AND b.principal_id=p.id AND b.scope_type='project' AND b.scope_id=p_project AND r.key='admin') THEN 0 ELSE 1 END,
   (SELECT min(b.created_at) FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
    WHERE b.tenant_id=p.tenant_id AND b.principal_id=p.id
    AND (b.scope_type='project' AND b.scope_id=p_project AND r.key='admin'
      OR b.scope_type='workspace' AND r.key IN ('owner','admin'))),p.id
 LIMIT 1
$$;

CREATE FUNCTION aeon_person_steps(p_node uuid) RETURNS jsonb LANGUAGE plpgsql STABLE AS $$
DECLARE
 n record; d record; block record; rule jsonb; mark jsonb; assigned jsonb;
 result jsonb := '[]'; step jsonb; person_id uuid; preferred uuid; fallback uuid;
 next_status text; current_status text; label_list text[]; checked boolean; applies boolean;
 condition text; text_value text; definition_count integer;
BEGIN
 SELECT work.*,k.slug,k.field_schema INTO n FROM nodes work
 JOIN node_kinds k ON k.tenant_id=work.tenant_id AND k.id=work.kind_id
 WHERE work.id=p_node AND work.deleted_at IS NULL AND k.slug IN ('work','ticket','task');
 IF NOT FOUND THEN RETURN result; END IF;
 current_status := replace(replace(lower(btrim(n.state)),' ','_'),'-','_');
 IF current_status<>'qa' THEN current_status:=aeon_work_status_category(n.state,n.field_schema); END IF;
 next_status := CASE WHEN current_status IN ('new','backlog','open','blocked') THEN 'in_progress'
   WHEN current_status IN ('in_progress','progress','active') THEN 'qa' WHEN current_status='qa' THEN 'done' END;
 SELECT array_agg(lower(CASE WHEN jsonb_typeof(x)='object' THEN x->>'name' ELSE x#>>'{}' END)) INTO label_list
 FROM jsonb_array_elements(
   CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END ||
   CASE WHEN jsonb_typeof(n.fields->'labels')='array' THEN n.fields->'labels' ELSE '[]'::jsonb END) x;
 label_list := coalesce(label_list,'{}');
 SELECT def.id,def.updated_at,def.fields,count(*) OVER() AS copies INTO d
 FROM nodes def JOIN node_kinds k ON k.tenant_id=def.tenant_id AND k.id=def.kind_id
 WHERE k.slug='definition' AND def.deleted_at IS NULL AND def.parent_id IS NOT DISTINCT FROM n.project_id
 ORDER BY def.id LIMIT 1;
 IF NOT FOUND AND n.project_id IS NOT NULL THEN
   SELECT def.id,def.updated_at,def.fields,count(*) OVER() AS copies INTO d
   FROM nodes def JOIN node_kinds k ON k.tenant_id=def.tenant_id AND k.id=def.kind_id
   WHERE k.slug='definition' AND def.deleted_at IS NULL AND def.parent_id IS NULL ORDER BY def.id LIMIT 1;
 END IF;
 IF d.id IS NOT NULL THEN
   IF d.copies<>1 OR octet_length(d.fields::text)>65536 OR jsonb_typeof(d.fields->'checks')<>'object' THEN
     RAISE EXCEPTION 'invalid or duplicate person-step definition';
   END IF;
   definition_count:=0;
   FOR block IN SELECT key,value FROM jsonb_each(d.fields->'checks') LOOP
     IF block.key NOT IN ('in_progress','qa','done') OR jsonb_typeof(block.value)<>'array' OR jsonb_array_length(block.value)>32 THEN
       RAISE EXCEPTION 'invalid bounded person-step block';
     END IF;
     FOR rule IN SELECT value FROM jsonb_array_elements(block.value) LOOP
       IF coalesce(rule->>'id','') NOT LIKE 'manual:%' THEN CONTINUE; END IF;
       definition_count:=definition_count+1;
       IF definition_count>96 THEN RAISE EXCEPTION 'person-step coverage exceeded'; END IF;
       condition:=coalesce(rule->>'condition','all');
       applies:=CASE condition
         WHEN 'ui' THEN 'ui'=ANY(label_list) OR n.fields->>'area'='frontend'
         WHEN 'code' THEN coalesce(n.fields->>'no_release_needed','false')<>'true' AND NOT ('docs'=ANY(label_list) OR 'content'=ANY(label_list))
         WHEN 'blocked' THEN current_status='blocked'
         WHEN 'release_notes' THEN coalesce(n.fields->>'hide_from_release_notes','false')<>'true'
         WHEN 'releases' THEN coalesce(n.fields->>'no_release_needed','false')<>'true' AND EXISTS(SELECT 1 FROM nodes r JOIN node_kinds k ON k.tenant_id=r.tenant_id AND k.id=r.kind_id WHERE r.project_id=n.project_id AND r.deleted_at IS NULL AND k.slug='release')
         ELSE true END;
       IF jsonb_array_length(coalesce(rule->'kinds','[]'))>0 THEN applies:=applies AND rule->'kinds' ? n.slug; END IF;
       IF EXISTS(SELECT 1 FROM jsonb_array_elements_text(coalesce(rule->'labels','[]')) v WHERE NOT lower(v)=ANY(label_list)) THEN applies:=false; END IF;
       IF NOT coalesce(applies,false) THEN CONTINUE; END IF;
       SELECT e.after INTO assigned FROM events e WHERE e.node_id=n.id AND e.type='node.person_step_reassigned'
         AND e.after->>'check_id'=rule->>'id' AND e.after->>'definition_id'=d.id::text
         AND (e.after->>'definition_revision')::timestamptz=d.updated_at ORDER BY e.id DESC LIMIT 1;
       preferred:=NULL; fallback:=NULL;
       IF assigned IS NOT NULL THEN preferred:=(assigned->>'owner_id')::uuid;
       ELSIF rule->>'owner_id' IS NOT NULL THEN preferred:=(rule->>'owner_id')::uuid;
       ELSE
         IF coalesce(n.fields->>'assignee','') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN preferred:=(n.fields->>'assignee')::uuid; END IF;
         fallback:=(rule->>'fallback_owner_id')::uuid;
       END IF;
       person_id:=aeon_person_step_owner(n.project_id,preferred,fallback);
       IF person_id IS NULL THEN RAISE EXCEPTION 'person step has no current person owner'; END IF;
       SELECT e.after INTO mark FROM events e WHERE e.node_id=n.id AND e.type IN ('entry_check.ticked','node.entry_check_ticked')
         AND e.after->>'check_id'=rule->>'id' AND e.after->>'definition_id'=d.id::text
         AND (e.after->>'definition_revision')::timestamptz=d.updated_at ORDER BY e.id DESC LIMIT 1;
       checked:=coalesce((mark->>'checked')::boolean,false);
       step:=jsonb_build_object('id',md5(n.id::text||':'||(rule->>'id')||':'||d.updated_at::text)::uuid,
         'node_id',n.id,'check_id',rule->>'id','label',rule->>'label','label_de',coalesce(rule->>'label_de',''),
         'to_status',block.key,'owner',(SELECT jsonb_build_object('id',id,'name',name) FROM principals WHERE id=person_id),
         'checked',checked,'active',NOT checked AND block.key=next_status,'revision',n.updated_at,
         'definition_id',d.id,'definition_revision',d.updated_at,'created_at',n.created_at,
         'ticket_key',n.key,'ticket_title',n.title,'project_id',n.project_id);
       result:=result||jsonb_build_array(step);
     END LOOP;
   END LOOP;
 END IF;
 text_value:=coalesce(n.human_check,n.fields->'human_check_completed'->>'text');
 IF text_value IS NOT NULL AND btrim(text_value)<>'' THEN
   SELECT e.after INTO assigned FROM events e WHERE e.node_id=n.id AND e.type='node.person_step_reassigned'
     AND e.after->>'check_id'='human' ORDER BY e.id DESC LIMIT 1;
   preferred:=NULL;
   IF assigned IS NOT NULL THEN preferred:=(assigned->>'owner_id')::uuid;
   ELSIF coalesce(n.fields->>'assignee','') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN preferred:=(n.fields->>'assignee')::uuid;
   END IF;
   person_id:=aeon_person_step_owner(n.project_id,preferred,NULL);
   IF person_id IS NULL THEN RAISE EXCEPTION 'human check has no current person owner'; END IF;
   checked:=n.human_check IS NULL;
   result:=result||jsonb_build_array(jsonb_build_object(
     'id',md5(n.id::text||':human:'||text_value)::uuid,'node_id',n.id,'check_id','human','label',text_value,'label_de','',
     'to_status','delivered','owner',(SELECT jsonb_build_object('id',id,'name',name) FROM principals WHERE id=person_id),
     'checked',checked,'active',NOT checked AND current_status='done','revision',n.updated_at,
     'definition_id',NULL,'definition_revision',NULL,'created_at',n.created_at,'ticket_key',n.key,'ticket_title',n.title,'project_id',n.project_id));
 END IF;
 RETURN result;
END
$$;

CREATE INDEX person_step_events_idx ON events(tenant_id,node_id,type,id DESC)
 WHERE type IN ('node.person_step_reassigned','entry_check.ticked','node.entry_check_ticked');
