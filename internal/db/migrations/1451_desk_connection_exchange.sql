-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1709 expansion: legacy grant writers remain valid.
SET LOCAL lock_timeout = '5s';
ALTER TABLE desk_instance_connections ADD COLUMN exchange_code_hash bytea;
ALTER TABLE desk_instance_connections ADD COLUMN pkce_challenge text;
ALTER TABLE desk_instance_connections ADD COLUMN exchange_expires_at timestamptz;
ALTER TABLE desk_instance_connections ADD COLUMN exchange_used_at timestamptz;
CREATE UNIQUE INDEX desk_instance_connections_exchange ON desk_instance_connections (tenant_id, exchange_code_hash);
