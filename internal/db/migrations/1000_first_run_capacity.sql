-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_runs ADD COLUMN capacity_override text NOT NULL DEFAULT ''
    CHECK (capacity_override IN ('', 'now'));
-- Accept the normalized stop signal; adapters supply it in AEON-292 T3.
ALTER TABLE run_telemetry DROP CONSTRAINT run_telemetry_error_code_check;
ALTER TABLE run_telemetry ADD CONSTRAINT run_telemetry_error_code_check
    CHECK (error_code IN ('event_stream_bound', 'app_server_protocol',
        'child_exit_failed', 'turn_failed', 'child_stop_failed', 'ownership_lost',
        'reporter_unavailable', 'workspace_conflict', 'decision_refused', 'vendor_limit'))
    NOT VALID;
