-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
CREATE TABLE model_profile_retirements (
 tenant_id uuid NOT NULL REFERENCES tenants(id), profile_id uuid NOT NULL,
 reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 200),
 retired_by uuid NOT NULL, retired_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,profile_id),
 FOREIGN KEY (tenant_id,profile_id) REFERENCES model_profiles(tenant_id,id),
 FOREIGN KEY (tenant_id,retired_by) REFERENCES principals(tenant_id,id)
);
ALTER TABLE model_profile_retirements ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_profile_retirements FORCE ROW LEVEL SECURITY;
CREATE POLICY model_profile_retirements_tenant ON model_profile_retirements
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE TRIGGER model_profile_retirements_no_update BEFORE UPDATE ON model_profile_retirements
 FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
