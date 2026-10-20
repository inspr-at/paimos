-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1346: production receipt authority needs original command metadata and
-- an exact, non-renewable final-only grant. Projection receipts cannot serve as
-- their own authority. Neither table retains input/final text or proof tokens.
CREATE TABLE native_session_operations (
    tenant_id uuid,
    operation_id uuid,
    owned_session_id uuid,
    project_id uuid,
    owner_person_id uuid,
    intent jsonb,
    created_at timestamptz,
    PRIMARY KEY (tenant_id,operation_id)
);
CREATE TABLE native_session_drain_grants (
    tenant_id uuid,
    grant_id uuid,
    owned_session_id uuid,
    project_id uuid,
    owner_person_id uuid,
    actor_grant_id uuid,
    actor_grant_revision bigint,
    retention_grant_id uuid,
    binding jsonb,
    conversation_ref text,
    conversation_id uuid,
    role_id uuid,
    issued_at timestamptz,
    expires_at timestamptz,
    maximum_finals bigint,
    maximum_bytes bigint,
    PRIMARY KEY (tenant_id,grant_id)
);
ALTER TABLE native_session_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_operations FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_operations_tenant ON native_session_operations
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY native_session_operations_project ON native_session_operations AS RESTRICTIVE
    USING (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_operations.tenant_id AND n.id=native_session_operations.project_id))
    WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_operations.tenant_id AND n.id=native_session_operations.project_id));
ALTER TABLE native_session_drain_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_drain_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_drain_grants_tenant ON native_session_drain_grants
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY native_session_drain_grants_project ON native_session_drain_grants AS RESTRICTIVE
    USING (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_drain_grants.tenant_id AND n.id=native_session_drain_grants.project_id))
    WITH CHECK (EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=native_session_drain_grants.tenant_id AND n.id=native_session_drain_grants.project_id));
CREATE TRIGGER native_session_operation_immutable BEFORE UPDATE OR DELETE ON native_session_operations
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
CREATE TRIGGER native_session_drain_immutable BEFORE UPDATE OR DELETE ON native_session_drain_grants
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
