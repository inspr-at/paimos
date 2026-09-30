-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Existing computers deliberately have no key: they keep Aeon approval until
-- a new browser-approved pairing pins an enclave public key. Registration can
-- report capability but cannot create or replace this authority.
ALTER TABLE agent_pairing_computers ADD COLUMN local_auth_public_key text NOT NULL DEFAULT '';
ALTER TABLE agent_pairing_computers ADD CONSTRAINT pairing_local_auth_public_key_format
 CHECK (local_auth_public_key = '' OR local_auth_public_key ~ '^[A-Za-z0-9+/]{87}=$');
ALTER TABLE harness_attach_requests ADD COLUMN local_auth_nonce text;
ALTER TABLE harness_attach_requests ADD CONSTRAINT attach_local_auth_nonce_format
 CHECK (local_auth_nonce IS NULL OR local_auth_nonce ~ '^[0-9a-f]{64}$');
-- In-flight approvals from the boolean protocol cannot survive the upgrade.
-- The production migrator owns the tables without bypassing FORCE RLS.
-- Visit tenants explicitly and open only the project visibility needed here.
DO $$
DECLARE
 target record;
 prior_tenant text := current_setting('aeon.tenant_id',true);
 prior_visibility text := current_setting('aeon.visible_projects',true);
BEGIN
 PERFORM set_config('aeon.visible_projects','*',true);
 FOR target IN SELECT id FROM tenants LOOP
  PERFORM set_config('aeon.tenant_id',target.id::text,true);
  WITH ended AS (
   UPDATE harness_attach_requests SET state='detached',lease_until=NULL
   WHERE tenant_id=target.id AND consent_mode='local_auth' AND state IN ('pending','approved','active')
   RETURNING session_id
  )
  UPDATE harness_sessions SET phase='stopped',stopped_at=coalesce(stopped_at,clock_timestamp()),
   stop_reason='Touch ID pairing upgrade; fresh approval required'
   WHERE tenant_id=target.id AND id IN (SELECT session_id FROM ended);
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
 PERFORM set_config('aeon.visible_projects',coalesce(prior_visibility,''),true);
END;
$$;
