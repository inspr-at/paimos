-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Forced tenant/project RLS on harness_sessions is unchanged. Legacy sessions
-- with no unambiguous owner remain admin-only for manual moves.
ALTER TABLE harness_sessions ADD COLUMN owner_principal_id uuid,
    ADD CONSTRAINT harness_session_owner_tenant FOREIGN KEY (tenant_id,owner_principal_id)
    REFERENCES principals(tenant_id,id);
