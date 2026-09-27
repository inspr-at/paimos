-- SPDX-License-Identifier: AGPL-3.0-only
-- Preserve the reason for cancelling an unclaimed verification without changing
-- its immutable run binding or replenishing its one-shot authority.
ALTER TABLE agent_pairing_enrollments ADD COLUMN verification_expired_at timestamptz;
ALTER TABLE agent_pairing_computers DROP CONSTRAINT agent_pairing_computers_setup_error_check;
ALTER TABLE agent_pairing_computers ADD CONSTRAINT agent_pairing_computers_setup_error_check
 CHECK (setup_error IN ('','login_required','service_conflict','unsupported_platform','managed_installation','connectivity_failed','private_storage_failed','installation_failed','verification_unavailable'));
