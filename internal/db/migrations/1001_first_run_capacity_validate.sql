-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-353. Validate the telemetry code check added NOT VALID in 1000.
-- SHARE UPDATE EXCLUSIVE validation permits normal reads and writes.
SET LOCAL lock_timeout = '5s';

ALTER TABLE run_telemetry VALIDATE CONSTRAINT run_telemetry_error_code_check;
