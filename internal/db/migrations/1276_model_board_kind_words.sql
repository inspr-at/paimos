-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- New tenants use these defaults. Existing hints stay untouched for the admin check.
CREATE FUNCTION aeon_seed_model_board_kinds(p_tenant uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id',true),'')::uuid THEN RAISE EXCEPTION 'tenant mismatch'; END IF;
 INSERT INTO work_kinds(tenant_id,slug,label,hint,system,position,examples,labels)
 SELECT p_tenant,v.* FROM (VALUES
 ('design','UI design','Screens and interaction, designed as an HTML mock before any code.',NULL::text,0,ARRAY['A settings screen','A ticket detail layout'],ARRAY['ui','ux']),
 ('frontend','Frontend build','Vue code that implements an approved design.',NULL,1,ARRAY['A Vue component','A board interaction'],ARRAY['frontend']),
 ('backend','Backend build','Go code, APIs, SQL and migrations behind the app.',NULL,2,ARRAY['An API handler','A SQL migration'],ARRAY['backend']),
 ('infra','Infrastructure','CI, Nix, hosting and deployments.',NULL,3,ARRAY['A CI check','A Nix change'],ARRAY['infrastructure']),
 ('docs','Docs and copy','Documentation and the words in PAIMOS.',NULL,4,ARRAY['A guide','Release copy'],ARRAY['documentation']),
 ('security','Security','Permissions and isolation, with a security review.','security',5,ARRAY['An authorization check','Tenant isolation'],ARRAY['security']),
 ('review','Reviews','Checks finished work before it merges.','review',6,ARRAY[]::text[],ARRAY[]::text[]),
 ('other','Everything else','Any ticket no other kind describes, and every kind without its own column.','other',7,ARRAY['Tidy up a script','A one-off data fix'],ARRAY[]::text[])
 ) v(slug,label,hint,system,position,examples,labels) WHERE NOT EXISTS(SELECT 1 FROM work_kinds k WHERE k.tenant_id=p_tenant AND k.slug=v.slug AND k.project_id IS NULL AND k.archived_at IS NULL);
END;
$$;
