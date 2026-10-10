-- SPDX-License-Identifier: AGPL-3.0-only
-- Imported creation times have a fixed ISO date and explicit timezone. Other
-- spellings retain event time; parsing never depends on the session timezone.
CREATE FUNCTION aeon_activity_at(typ text, doc jsonb, event_at timestamptz) RETURNS timestamptz
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT CASE WHEN typ='import.node_created'
 AND doc->>'created_at' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$'
 AND pg_input_is_valid(doc->>'created_at','timestamptz')
 THEN (doc->>'created_at')::timestamptz ELSE event_at END
$$;
CREATE FUNCTION aeon_activity_source(typ text, doc jsonb) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT CASE WHEN strpos(coalesce(doc->>'classic_ref',''),':'||typ||':')>0
 THEN split_part(doc->>'classic_ref',':'||typ||':',1)
 ELSE coalesce(doc->'fields'->'classic'->>'source_id','') END
$$;
CREATE INDEX events_activity_page ON events(tenant_id,node_id,aeon_activity_at(type,after,at) DESC,id DESC)
 WHERE type IN ('import.comment','import.history','import.node_created','node.created','node.updated','node.moved','node.kind_changed','comment.created','status_autopilot.changed','status_autopilot.undone','status_autopilot.skipped','status_autopilot.derived','status_autopilot.retained','status_autopilot.causal_undo');
CREATE INDEX events_activity_comment_revision ON events(tenant_id,node_id,(after->>'comment_id'),id DESC)
 WHERE type IN ('comment.updated','comment.deleted');
CREATE INDEX events_activity_import_comment ON events(tenant_id,node_id,aeon_activity_source(type,after),(coalesce(nullif(after->'record'->>'id',''),id::text)),id)
 WHERE type='import.comment';
CREATE INDEX events_activity_import_history ON events(tenant_id,node_id,aeon_activity_source(type,after),at DESC,id DESC)
 WHERE type='import.history' AND jsonb_typeof(after->'record'->'snapshot')='object';
