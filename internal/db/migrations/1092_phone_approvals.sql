-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-455: phone decisions use the existing approval/attach stores, never push capabilities.
CREATE TABLE phone_approval_preferences (
 tenant_id uuid NOT NULL REFERENCES tenants(id), person_id uuid NOT NULL,
 enabled boolean NOT NULL DEFAULT false, time_zone text NOT NULL DEFAULT 'UTC',
 quiet_start integer NOT NULL DEFAULT 0 CHECK (quiet_start BETWEEN 0 AND 1439),
 quiet_end integer NOT NULL DEFAULT 0 CHECK (quiet_end BETWEEN 0 AND 1439),
 escalation_minutes integer NOT NULL DEFAULT 15 CHECK (escalation_minutes BETWEEN 5 AND 1440),
 PRIMARY KEY (tenant_id,person_id), FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
CREATE TABLE phone_passkeys (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 person_id uuid NOT NULL, credential_id text NOT NULL, credential jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz,
 PRIMARY KEY (tenant_id,id), UNIQUE (tenant_id,credential_id),
 FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
CREATE TABLE phone_push_subscriptions (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 person_id uuid NOT NULL, endpoint_hash text NOT NULL, subscription bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz,
 PRIMARY KEY (tenant_id,id), UNIQUE (tenant_id,endpoint_hash),
 FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
CREATE TABLE phone_approval_challenges (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL DEFAULT gen_random_uuid(),
 person_id uuid NOT NULL, kind text NOT NULL CHECK (kind IN ('registration','approval','attach')),
 request_id uuid, binding text NOT NULL, session_data jsonb NOT NULL,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '2 minutes', consumed_at timestamptz,
 PRIMARY KEY (tenant_id,id), FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
CREATE INDEX phone_challenges_person ON phone_approval_challenges(tenant_id,person_id,expires_at);
CREATE TABLE phone_approval_limits (
 tenant_id uuid NOT NULL REFERENCES tenants(id), person_id uuid NOT NULL,
 started_at timestamptz NOT NULL DEFAULT now(), attempts integer NOT NULL DEFAULT 1,
 PRIMARY KEY (tenant_id,person_id), FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
CREATE TABLE phone_push_deliveries (
 tenant_id uuid NOT NULL REFERENCES tenants(id), kind text NOT NULL CHECK (kind IN ('approval','attach')),
 request_id uuid NOT NULL, person_id uuid NOT NULL, subscription_id uuid NOT NULL,
 sent_at timestamptz, retry_at timestamptz NOT NULL DEFAULT now(), attempts integer NOT NULL DEFAULT 0,
 last_status integer NOT NULL DEFAULT 0,
 PRIMARY KEY (tenant_id,kind,request_id,subscription_id),
 FOREIGN KEY (tenant_id,subscription_id) REFERENCES phone_push_subscriptions(tenant_id,id),
 FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id)
);
ALTER TABLE phone_approval_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_approval_preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY phone_preferences_tenant ON phone_approval_preferences USING (tenant_id=current_setting('aeon.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('aeon.tenant_id',true)::uuid);
ALTER TABLE phone_passkeys ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_passkeys FORCE ROW LEVEL SECURITY;
CREATE POLICY phone_passkeys_tenant ON phone_passkeys USING (tenant_id=current_setting('aeon.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('aeon.tenant_id',true)::uuid);
ALTER TABLE phone_push_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_push_subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY phone_subscriptions_tenant ON phone_push_subscriptions USING (tenant_id=current_setting('aeon.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('aeon.tenant_id',true)::uuid);
ALTER TABLE phone_approval_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_approval_challenges FORCE ROW LEVEL SECURITY;
CREATE POLICY phone_challenges_tenant ON phone_approval_challenges USING (tenant_id=current_setting('aeon.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('aeon.tenant_id',true)::uuid);
ALTER TABLE phone_approval_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_approval_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY phone_limits_tenant ON phone_approval_limits USING (tenant_id=current_setting('aeon.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('aeon.tenant_id',true)::uuid);
ALTER TABLE phone_push_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_push_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY phone_deliveries_tenant ON phone_push_deliveries USING (tenant_id=current_setting('aeon.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('aeon.tenant_id',true)::uuid);
