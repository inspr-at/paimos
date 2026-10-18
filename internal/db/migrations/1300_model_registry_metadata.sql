-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Nullable fields preserve old writers and immutable model history.
ALTER TABLE model_profiles ADD COLUMN source text, ADD COLUMN note text, ADD COLUMN registered_effort_level smallint;
ALTER TABLE model_profile_retirements ADD COLUMN retire_at timestamptz, ADD COLUMN applied_at timestamptz;
ALTER TABLE model_refresh_settings ADD COLUMN last_manual_run_at timestamptz;
CREATE INDEX model_scheduled_retirements ON model_profile_retirements(tenant_id,retire_at) WHERE retire_at IS NOT NULL AND applied_at IS NULL;
