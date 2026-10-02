-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_keys ADD COLUMN owner_workstation boolean NOT NULL DEFAULT false;
ALTER TABLE agent_keys ADD COLUMN workstation_computer_id uuid;
ALTER TABLE agent_keys ADD COLUMN workstation_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE agent_keys ADD CONSTRAINT owner_workstation_computer
  CHECK (owner_workstation = (workstation_computer_id IS NOT NULL));
ALTER TABLE agent_keys ADD FOREIGN KEY (tenant_id,workstation_computer_id)
  REFERENCES agent_pairing_computers(tenant_id,id);
-- The owner's decision is one key per tracker, stronger than one per computer.
-- Expired keys must be explicitly unmarked before replacing the designation.
CREATE UNIQUE INDEX owner_workstation_one_per_tenant ON agent_keys(tenant_id)
  WHERE owner_workstation AND revoked_at IS NULL;

CREATE TABLE owner_workstation_challenges (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  key_id uuid NOT NULL,
  principal_id uuid NOT NULL,
  computer_id uuid NOT NULL,
  nonce text NOT NULL CHECK (nonce ~ '^[0-9a-f]{64}$'),
  action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
  public_key text NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,id),
  UNIQUE (tenant_id,key_id),
  FOREIGN KEY (tenant_id,key_id) REFERENCES agent_keys(tenant_id,id),
  FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id),
  FOREIGN KEY (tenant_id,computer_id) REFERENCES agent_pairing_computers(tenant_id,id)
);
-- One pending challenge per key bounds storage. Successful verification deletes
-- it atomically; failed/replayed proofs never reach a handler. No bodies retained.
ALTER TABLE owner_workstation_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE owner_workstation_challenges FORCE ROW LEVEL SECURITY;
CREATE POLICY owner_workstation_challenges_tenant ON owner_workstation_challenges
  USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Keep the database approval fence, extending it only for the exact marked
-- request key. The agent may never decide its own request, under any key.
CREATE OR REPLACE FUNCTION aeon_guard_approval() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor_kind text; target_kind text; expiry timestamptz; target_agent uuid; marked boolean;
BEGIN
  IF TG_TABLE_NAME = 'approval_requests' THEN
    SELECT kind INTO actor_kind FROM principals WHERE tenant_id=NEW.tenant_id AND id=NEW.proposed_by_principal_id;
    SELECT kind INTO target_kind FROM principals WHERE tenant_id=NEW.tenant_id AND id=NEW.agent_principal_id;
    IF actor_kind IS DISTINCT FROM 'agent' OR target_kind IS DISTINCT FROM 'agent' OR NEW.proposed_by_principal_id<>NEW.agent_principal_id THEN
      RAISE EXCEPTION 'only an agent may propose its own permission';
    END IF;
  ELSE
    SELECT kind INTO actor_kind FROM principals WHERE tenant_id=NEW.tenant_id AND id=NEW.decided_by_principal_id;
    SELECT expires_at,agent_principal_id INTO expiry,target_agent FROM approval_requests WHERE tenant_id=NEW.tenant_id AND id=NEW.request_id;
    SELECT EXISTS(SELECT 1 FROM agent_keys k
      JOIN agent_pairing_computers c ON c.tenant_id=k.tenant_id AND c.id=k.workstation_computer_id AND c.principal_id=k.principal_id
      JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
      WHERE k.tenant_id=NEW.tenant_id AND k.id=NULLIF(current_setting('aeon.owner_workstation_key',true),'')::uuid
      AND k.principal_id=NEW.decided_by_principal_id AND k.owner_workstation AND k.revoked_at IS NULL
      AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND 'approvals.decide'=ANY(k.scopes)
      AND c.state='connected' AND q.state='redeemed' AND c.local_auth_public_key<>'') INTO marked;
    IF expiry IS NULL OR expiry<=clock_timestamp() OR
       (actor_kind IS DISTINCT FROM 'person' AND NOT (actor_kind='agent' AND marked AND target_agent<>NEW.decided_by_principal_id)) THEN
      RAISE EXCEPTION 'only a person or another marked agent may decide a live approval';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
