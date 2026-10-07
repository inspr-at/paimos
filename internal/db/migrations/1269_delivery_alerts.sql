-- SPDX-License-Identifier: AGPL-3.0-only
-- Expansion: older binaries have no writers for delivery_alerts.
SET LOCAL lock_timeout = '5s';
CREATE TABLE delivery_alerts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    item_id uuid NOT NULL,
    state text NOT NULL,
    state_since timestamptz NOT NULL,
    alerted_at timestamptz NOT NULL,
    recipient_principal_id uuid,
    inbox_message_id uuid,
    cleared_at timestamptz,
    PRIMARY KEY (tenant_id,item_id,state,state_since),
    FOREIGN KEY (tenant_id,recipient_principal_id) REFERENCES principals(tenant_id,id)
);
-- No FK to the rebuildable projection: deleting/replaying it must preserve
-- episode idempotency, including alerts for the pre-PR placeholder.
CREATE INDEX delivery_alerts_open ON delivery_alerts(tenant_id,item_id) WHERE cleared_at IS NULL;
ALTER TABLE delivery_alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_alerts FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_alerts_tenant ON delivery_alerts
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY delivery_alerts_project ON delivery_alerts AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM delivery_items i WHERE i.tenant_id=delivery_alerts.tenant_id AND i.id=delivery_alerts.item_id))
    WITH CHECK ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM delivery_items i WHERE i.tenant_id=delivery_alerts.tenant_id AND i.id=delivery_alerts.item_id));

-- Close episodes atomically on every existing projection write, including
-- holds and a new head in the same state. No inbox message or event is sent.
CREATE FUNCTION aeon_clear_delivery_alerts() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE delivery_alerts SET cleared_at=clock_timestamp()
    WHERE tenant_id=OLD.tenant_id AND item_id=OLD.id AND cleared_at IS NULL
        AND state=OLD.state AND state_since=OLD.state_since;
    RETURN NEW;
END;
$$;
CREATE TRIGGER delivery_alerts_state_changed BEFORE UPDATE OF state,state_since ON delivery_items
    FOR EACH ROW WHEN ((OLD.state,OLD.state_since) IS DISTINCT FROM (NEW.state,NEW.state_since))
    EXECUTE FUNCTION aeon_clear_delivery_alerts();
