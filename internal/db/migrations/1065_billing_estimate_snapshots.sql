-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-511: additive, unknown until explicitly declared by a person.
ALTER TABLE agent_accounts ADD COLUMN IF NOT EXISTS billing_mode text NOT NULL DEFAULT 'unknown'
    CHECK (billing_mode IN ('unknown', 'api', 'subscription'));

CREATE TABLE ticket_estimate_snapshots (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    ticket_node_id uuid NOT NULL,
    source_project_id uuid,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, ticket_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, source_project_id) REFERENCES nodes(tenant_id, id)
);
CREATE UNIQUE INDEX ticket_estimate_snapshots_active ON ticket_estimate_snapshots(tenant_id, ticket_node_id)
    WHERE closed_at IS NULL;
CREATE INDEX ticket_estimate_snapshots_history ON ticket_estimate_snapshots(tenant_id, ticket_node_id, started_at DESC, id DESC);
ALTER TABLE ticket_estimate_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_estimate_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY ticket_estimate_snapshots_tenant ON ticket_estimate_snapshots
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- Only closing the episode may update a row; the baseline is immutable.
CREATE TRIGGER ticket_estimate_snapshots_immutable
    BEFORE UPDATE OF tenant_id, id, ticket_node_id, source_project_id, started_at, snapshot ON ticket_estimate_snapshots
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
CREATE TRIGGER ticket_estimate_snapshots_no_delete BEFORE DELETE ON ticket_estimate_snapshots
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
