-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1629: old inbox writers leave this nullable payload absent.
ALTER TABLE inbox_messages ADD COLUMN lane_status jsonb;

-- A bounded current projection, separate from the durable inbox history.
-- The pointer is reserved before the first event append, then its NEW message
-- is inserted in the same transaction. The deferred FK checks only that new
-- row; no existing-row lock is taken after the event counter.
CREATE TABLE inbox_lane_statuses (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    session_id uuid NOT NULL,
    target_node_id uuid NOT NULL,
    sender_principal_id uuid NOT NULL,
    message_id uuid NOT NULL,
    reported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, session_id, target_node_id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, target_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, sender_principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, message_id) REFERENCES inbox_messages(tenant_id, id)
        ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX inbox_lane_statuses_target ON inbox_lane_statuses(tenant_id, target_node_id, session_id);
ALTER TABLE inbox_lane_statuses ENABLE ROW LEVEL SECURITY;
ALTER TABLE inbox_lane_statuses FORCE ROW LEVEL SECURITY;
CREATE POLICY inbox_lane_statuses_tenant ON inbox_lane_statuses
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY inbox_lane_statuses_visible ON inbox_lane_statuses AS RESTRICTIVE FOR SELECT
    USING (EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id=inbox_lane_statuses.tenant_id AND n.id=inbox_lane_statuses.target_node_id)
       AND EXISTS (SELECT 1 FROM harness_sessions s WHERE s.tenant_id=inbox_lane_statuses.tenant_id AND s.id=inbox_lane_statuses.session_id));
