-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1304: isolated, content-free interaction projections. Live native
-- callbacks and plaintext control capabilities are never reconstructed.
CREATE TABLE native_session_controllers (
    tenant_id uuid,
    owned_session_id uuid,
    actor_person_id uuid,
    client_id uuid,
    viewer_id uuid,
    epoch uuid,
    boot_id uuid,
    binding_digest bytea,
    capability_digest bytea,
    expires_at timestamptz,
    PRIMARY KEY (tenant_id,owned_session_id)
);
CREATE TABLE native_session_viewers (
    tenant_id uuid,
    id uuid,
    owned_session_id uuid,
    actor_person_id uuid,
    client_id uuid,
    request_id uuid,
    request_digest bytea,
    binding_digest bytea,
    boot_id uuid,
    mode text,
    view_kind text,
    consent_id uuid,
    controller_epoch uuid,
    expires_at timestamptz,
    detached_at timestamptz,
    PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX native_session_viewer_request ON native_session_viewers(tenant_id,request_id);
CREATE INDEX native_session_viewer_bound ON native_session_viewers(tenant_id,owned_session_id,expires_at);
CREATE TABLE native_session_interactions (
    tenant_id uuid,
    id uuid,
    owned_session_id uuid,
    actor_person_id uuid,
    requester_principal_id uuid,
    request_id uuid,
    request_digest bytea,
    binding_digest bytea,
    boot_id uuid,
    turn_ref text,
    native_request_ref text,
    kind text,
    effect_digest text,
    desk_item_id uuid,
    step_up_required boolean,
    expires_at timestamptz,
    created_at timestamptz,
    decision_request_id uuid,
    decision_digest bytea,
    decided_by uuid,
    resolved_at timestamptz,
    PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX native_session_interaction_request ON native_session_interactions(tenant_id,request_id);
CREATE INDEX native_session_interaction_pending ON native_session_interactions(tenant_id,owned_session_id,expires_at) WHERE resolved_at IS NULL;

ALTER TABLE native_session_controllers ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_controllers FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_controllers_tenant ON native_session_controllers
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE native_session_viewers ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_viewers FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_viewers_tenant ON native_session_viewers
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE native_session_interactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_interactions FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_interactions_tenant ON native_session_interactions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
