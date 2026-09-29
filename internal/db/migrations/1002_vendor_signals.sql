-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE run_telemetry DROP CONSTRAINT run_telemetry_error_code_check;
ALTER TABLE run_telemetry ADD CONSTRAINT run_telemetry_error_code_check CHECK (error_code IN (
    'event_stream_bound', 'app_server_protocol', 'child_exit_failed', 'turn_failed',
    'child_stop_failed', 'ownership_lost', 'reporter_unavailable', 'workspace_conflict',
    'decision_refused', 'vendor_limit'));

ALTER TABLE run_telemetry ADD COLUMN limit_window text NOT NULL DEFAULT '' CHECK (limit_window IN ('','5h','weekly','monthly','other'));
ALTER TABLE run_telemetry ADD COLUMN limit_resets_at timestamptz;
ALTER TABLE agent_accounts ADD COLUMN reading_support text NOT NULL DEFAULT 'none' CHECK (reading_support IN ('every_5_min','first_run','statusline','none'));
ALTER TABLE agent_accounts ADD COLUMN quota_fingerprint text NOT NULL DEFAULT '' CHECK (quota_fingerprint = '' OR quota_fingerprint ~ '^[a-f0-9]{64}$');
ALTER TABLE agent_accounts ADD COLUMN statusline_enabled boolean NOT NULL DEFAULT false;
