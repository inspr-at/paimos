-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only: principal kind stays person/agent, and older writers omit the
-- optional class. Legacy messaging assistants are read as the assistant class
-- with a conservative default policy, without rewriting their identities/keys.
ALTER TABLE principals ADD COLUMN agent_class text;
CREATE INDEX principals_assistant_class ON principals(tenant_id, id)
    WHERE kind='agent' AND (agent_class='assistant' OR messaging_only IS TRUE);

CREATE TABLE assistant_profiles (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    principal_id uuid NOT NULL,
    owner_person_id uuid NOT NULL,
    vendor text NOT NULL,
    policy jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, principal_id),
    FOREIGN KEY (tenant_id, principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, owner_person_id) REFERENCES principals(tenant_id, id)
);
CREATE INDEX assistant_profiles_owner ON assistant_profiles(tenant_id, owner_person_id, principal_id);
ALTER TABLE assistant_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE assistant_profiles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON assistant_profiles
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
