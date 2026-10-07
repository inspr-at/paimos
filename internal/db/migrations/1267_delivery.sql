-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion: these tables have no writers in older binaries.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_items (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 project_id uuid,
 ticket_node_id uuid,
 repository text NOT NULL,
 pull_request bigint,
 branch text NOT NULL DEFAULT '',
 head_sha text NOT NULL DEFAULT '',
 state text NOT NULL CHECK(state IN ('built','reviewed','pushed','ci_green','in_queue','merged','queue_failed','held')),
 state_since timestamptz NOT NULL,
 owner text NOT NULL,
 deadline_at timestamptz,
 held_reason text,
 link_source text CHECK(link_source IN ('review_row','title_key','branch_key')),
 observation jsonb NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,repository,pull_request),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY(tenant_id,ticket_node_id) REFERENCES nodes(tenant_id,id)
);
CREATE UNIQUE INDEX delivery_unpushed_ticket ON delivery_items(tenant_id,repository,ticket_node_id) WHERE pull_request IS NULL;
CREATE INDEX delivery_ticket ON delivery_items(tenant_id,ticket_node_id,id);
CREATE INDEX delivery_project_state ON delivery_items(tenant_id,project_id,state,id);
ALTER TABLE delivery_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_items FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_items_tenant ON delivery_items
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_items_project ON delivery_items AS RESTRICTIVE
 USING(((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 AND (ticket_node_id IS NULL OR EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=delivery_items.tenant_id AND n.id=ticket_node_id AND n.deleted_at IS NULL)))
 WITH CHECK(((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 AND (ticket_node_id IS NULL OR EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=delivery_items.tenant_id AND n.id=ticket_node_id AND n.deleted_at IS NULL)));
CREATE TABLE delivery_github_events (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 sequence bigint GENERATED ALWAYS AS IDENTITY,
 delivery_id text NOT NULL,
 event text NOT NULL,
 action text NOT NULL DEFAULT '',
 repository text NOT NULL,
 pull_request bigint,
 head_sha text NOT NULL DEFAULT '',
 received_at timestamptz NOT NULL,
 payload_sha256 text NOT NULL,
 observations jsonb NOT NULL DEFAULT '[]'::jsonb,
 fanout_done boolean NOT NULL DEFAULT false,
 processed_at timestamptz,
 PRIMARY KEY(tenant_id,delivery_id),
 UNIQUE(tenant_id,sequence)
);
ALTER TABLE delivery_github_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_github_events FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_github_events_tenant ON delivery_github_events
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE TABLE delivery_settings (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid,
 required_checks text[],
 deadlines jsonb NOT NULL DEFAULT '{}'::jsonb,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id)
);
CREATE UNIQUE INDEX delivery_settings_scope ON delivery_settings(tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid)));
ALTER TABLE delivery_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_settings_tenant ON delivery_settings
 USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid AND (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])))
 WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid AND (project_id IS NULL OR (SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[])));
