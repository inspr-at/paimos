-- SPDX-License-Identifier: AGPL-3.0-only
-- Cached input and reasoning counters (AEON-300).
-- Reasoning is a subset of output and is not added to estimated cost.
-- Session reasoning stays null when the vendor did not report it.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_runs ADD COLUMN cached_input_tokens bigint NOT NULL DEFAULT 0
  CHECK (cached_input_tokens BETWEEN 0 AND 1000000000000);
ALTER TABLE agent_runs ADD COLUMN reasoning_tokens bigint NOT NULL DEFAULT 0
  CHECK (reasoning_tokens BETWEEN 0 AND 1000000000000);
ALTER TABLE run_telemetry ADD COLUMN cached_input_tokens_delta bigint NOT NULL DEFAULT 0
  CHECK (cached_input_tokens_delta BETWEEN 0 AND 1000000000000);
ALTER TABLE run_telemetry ADD COLUMN reasoning_tokens_delta bigint NOT NULL DEFAULT 0
  CHECK (reasoning_tokens_delta BETWEEN 0 AND 1000000000000);
ALTER TABLE harness_session_usage ADD COLUMN reasoning_tokens bigint
  CHECK (reasoning_tokens IS NULL OR reasoning_tokens BETWEEN 0 AND 1000000000000);
ALTER TABLE harness_session_usage ADD CHECK (reasoning_tokens IS NULL OR output_tokens IS NULL OR reasoning_tokens <= output_tokens);
