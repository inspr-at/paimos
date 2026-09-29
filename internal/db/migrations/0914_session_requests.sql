-- SPDX-License-Identifier: AGPL-3.0-only
-- Session UUIDs are immutable registration generations. Never address by agent.
ALTER TABLE harness_controls DROP CONSTRAINT harness_controls_kind_check;
ALTER TABLE harness_controls ADD CONSTRAINT harness_controls_kind_check
    CHECK (kind IN ('interrupt','stop','force_stop','steer','rename_request','model_request'));
ALTER TABLE harness_controls
    ADD COLUMN request_payload jsonb,
    ADD COLUMN expected_generation uuid,
    ADD CONSTRAINT harness_session_request_identity CHECK (
        kind NOT IN ('rename_request','model_request') OR
        (request_payload IS NOT NULL AND jsonb_typeof(request_payload)='object'
         AND expected_generation IS NOT NULL AND expected_generation=session_id
         AND request_digest IS NOT NULL AND expires_at IS NOT NULL));
