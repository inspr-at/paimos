-- SPDX-License-Identifier: AGPL-3.0-only
-- Optional verification failure is separate from managed-work outcomes.
ALTER TABLE agent_runs ADD COLUMN verification_unavailable_reason text NOT NULL DEFAULT ''
  CHECK (verification_unavailable_reason IN ('','adapter_unsupported','binding_incomplete','local_binding_missing'));
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_verification_refusal_no_launch CHECK (
  verification_unavailable_reason='' OR
  (purpose='pairing_verification' AND status='failed' AND started_at IS NULL AND daemon_id IS NULL AND daemon_generation IS NULL)
);
