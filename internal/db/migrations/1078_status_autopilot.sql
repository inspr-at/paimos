-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-521B. Additive deterministic status automation; existing states are untouched.
SET LOCAL lock_timeout = '5s';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS human_check text;
ALTER TABLE nodes ADD COLUMN status_autopilot jsonb NOT NULL DEFAULT '{}';

CREATE TABLE status_autopilot_settings (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 enabled boolean NOT NULL DEFAULT true,
 rules jsonb NOT NULL CHECK (jsonb_typeof(rules)='object'),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0)
);
ALTER TABLE status_autopilot_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_settings_tenant ON status_autopilot_settings
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE TABLE status_autopilot_projects (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 mode text NOT NULL DEFAULT 'inherit' CHECK (mode IN ('inherit','on','off')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 PRIMARY KEY (tenant_id,project_id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE status_autopilot_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_projects_tenant ON status_autopilot_projects
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY status_autopilot_projects_visibility ON status_autopilot_projects AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

-- Receipts survive Undo: replaying a job must never redo a human's Undo.
CREATE TABLE status_autopilot_receipts (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 node_id uuid NOT NULL,
 rule text NOT NULL,
 anchor text NOT NULL,
 event_id bigint NOT NULL,
 PRIMARY KEY (tenant_id,node_id,rule,anchor),
 FOREIGN KEY (tenant_id,node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,event_id) REFERENCES events(tenant_id,id)
);
ALTER TABLE status_autopilot_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_receipts_tenant ON status_autopilot_receipts
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY status_autopilot_receipts_visibility ON status_autopilot_receipts AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM nodes n WHERE n.id=status_autopilot_receipts.node_id AND n.tenant_id=status_autopilot_receipts.tenant_id))
 WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.id=status_autopilot_receipts.node_id AND n.tenant_id=status_autopilot_receipts.tenant_id));

CREATE TABLE status_autopilot_days (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 day date NOT NULL
);
ALTER TABLE status_autopilot_days ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_days FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_days_tenant ON status_autopilot_days
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Suggestions and reminders belong to a status episode; a person changing
-- status clears them, so re-entry gets a fresh threshold and receipt.
CREATE FUNCTION aeon_status_autopilot_reset_flags() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state AND NEW.status_autopilot IS NOT DISTINCT FROM OLD.status_autopilot THEN
  NEW.status_autopilot := '{}'::jsonb;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_status_autopilot_reset_flags BEFORE UPDATE OF state ON nodes
 FOR EACH ROW EXECUTE FUNCTION aeon_status_autopilot_reset_flags();

-- Bounded daily scans resume at the last committed ticket after a restart.
ALTER TABLE status_autopilot_days ADD COLUMN running_day date;
ALTER TABLE status_autopilot_days ADD COLUMN after_node_id uuid;
CREATE INDEX nodes_status_autopilot_candidates ON nodes
 (tenant_id,id) WHERE deleted_at IS NULL AND
 regexp_replace(lower(btrim(state)), '[[:space:]-]+', '_', 'g')
 IN ('new','backlog','blocked','in_progress','inprogress','progress','active','done','delivered');
CREATE INDEX nodes_status_autopilot_marks ON nodes (tenant_id,id)
 WHERE deleted_at IS NULL AND status_autopilot<>'{}'::jsonb;

-- A release is discovered once. Its remaining deliveries survive bounded
-- transactions, human checks and a transient failure reading one ticket.
CREATE TABLE status_autopilot_releases (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 release_id uuid NOT NULL,
 PRIMARY KEY (tenant_id,release_id),
 FOREIGN KEY (tenant_id,release_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE status_autopilot_releases ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_releases FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_releases_tenant ON status_autopilot_releases
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE POLICY status_autopilot_releases_visibility ON status_autopilot_releases AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=status_autopilot_releases.tenant_id AND n.id=status_autopilot_releases.release_id))
 WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=status_autopilot_releases.tenant_id AND n.id=status_autopilot_releases.release_id));

CREATE TABLE status_autopilot_deliveries (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 release_id uuid NOT NULL,
 node_id uuid NOT NULL,
 checked boolean NOT NULL DEFAULT false,
 retry_after timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY (tenant_id,release_id,node_id),
 FOREIGN KEY (tenant_id,release_id) REFERENCES status_autopilot_releases(tenant_id,release_id),
 FOREIGN KEY (tenant_id,node_id) REFERENCES nodes(tenant_id,id)
);
CREATE INDEX status_autopilot_deliveries_pending ON status_autopilot_deliveries (tenant_id,retry_after,release_id,node_id);
ALTER TABLE status_autopilot_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_autopilot_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY status_autopilot_deliveries_tenant ON status_autopilot_deliveries
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY status_autopilot_deliveries_visibility ON status_autopilot_deliveries AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=status_autopilot_deliveries.tenant_id AND n.id=status_autopilot_deliveries.node_id))
 WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=status_autopilot_deliveries.tenant_id AND n.id=status_autopilot_deliveries.node_id));
