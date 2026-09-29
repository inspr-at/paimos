-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE harness_controls DROP CONSTRAINT harness_controls_kind_check;
ALTER TABLE harness_controls ADD CONSTRAINT harness_controls_kind_check CHECK (kind IN ('interrupt','stop','force_stop','steer'));
ALTER TABLE harness_controls ADD CONSTRAINT harness_steer_identity CHECK (kind <> 'steer' OR (expected_ownership IS NOT NULL AND request_digest IS NOT NULL AND expires_at IS NOT NULL));
