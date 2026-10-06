-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-738: measurement provenance, never an assignment or execution grant.
-- Old generations deliberately retain NULL. No historical attribution backfill.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions ADD COLUMN usage_lead_session_id uuid;
CREATE INDEX harness_usage_lead ON harness_sessions(tenant_id,usage_lead_session_id,created_at,id);

-- Snapshot the reported coordinator lineage at registration. Nested workers
-- inherit their parent's snapshot; unknown/legacy lineage stays unknown.
-- Reparenting, ticket moves and succession cannot relabel earlier counters.
CREATE FUNCTION aeon_harness_usage_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  NEW.usage_lead_session_id:=OLD.usage_lead_session_id;
 ELSIF NEW.role='coordinator' THEN
  NEW.usage_lead_session_id:=NEW.id;
 ELSE
  NEW.usage_lead_session_id:=NULL;
  SELECT usage_lead_session_id INTO NEW.usage_lead_session_id FROM harness_sessions
   WHERE tenant_id=NEW.tenant_id AND project_id=NEW.project_id AND id=NEW.parent_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER harness_usage_lineage BEFORE INSERT OR UPDATE ON harness_sessions
 FOR EACH ROW EXECUTE FUNCTION aeon_harness_usage_lineage();
