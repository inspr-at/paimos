-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-697: public attention resolutions use ordinary visible-node snapshots.
-- The released proposal schema is retained; visibility adds only named public events.
-- Preserve every hidden-reference check and derive parent statuses from leaves.
SET LOCAL lock_timeout = '5s';
ALTER POLICY events_project_visibility ON events
    USING ((SELECT aeon_visible_all())
        OR (CASE
                WHEN node_id IS NULL THEN
                    (SELECT aeon_visibility_system())
                    OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[])
                ELSE EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id = events.tenant_id AND n.id = events.node_id)
                    AND (split_part(type, '.', 1) IN ('node', 'nodes', 'comment', 'comments', 'attachment',
                            'attachments', 'relation', 'relations', 'import', 'journey', 'intake', 'requirement',
                            'requirements', 'release', 'releases', 'knowledge', 'view', 'views', 'tag', 'tags',
                            'kind', 'kinds', 'profile', 'recurrence')
                         OR type IN ('status_autopilot.proposed', 'status_autopilot.attention_apply', 'status_autopilot.attention_dismiss', 'status_autopilot.attention_undone', 'status_autopilot.changed', 'status_autopilot.undone', 'status_autopilot.skipped', 'status_autopilot.derived', 'status_autopilot.retained', 'status_autopilot.causal_undo')
                         OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[]))
            END
            AND (cardinality(node_refs) = 0
                 OR NOT EXISTS (SELECT 1 FROM unnest(node_refs) AS ref(id)
                                WHERE ref.id NOT IN (SELECT n.id FROM nodes n)))));

CREATE OR REPLACE FUNCTION aeon_work_status_cause() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('aeon.work_status_entered',true) IS DISTINCT FROM NEW.tenant_id::text THEN RETURN NEW; END IF;
 IF NEW.type IN ('node.created','node.updated','node.deleted','node.moved','node.project_moved','node.bulk_changed','node.kind_changed','import.node_created','import.node_updated','import.parent_changed','status_autopilot.attention_apply','status_autopilot.attention_dismiss','status_autopilot.attention_undone','status_autopilot.changed','status_autopilot.undone','kind.updated','feature.updated')
  AND (NEW.node_id IN (SELECT id FROM pg_temp.aeon_work_changes WHERE tenant_id=NEW.tenant_id)
    OR NEW.type IN ('node.bulk_changed','kind.updated','feature.updated')) THEN
  IF (SELECT count(*) FROM pg_temp.aeon_work_causes)>=10000 THEN
   RAISE EXCEPTION 'work status cause scope exceeds 10000 events' USING ERRCODE='54000';
  END IF;
  INSERT INTO pg_temp.aeon_work_causes VALUES(NEW.tenant_id,NEW.id,NEW.node_id,NEW.type);
 END IF;
 RETURN NEW;
END;
$$;
