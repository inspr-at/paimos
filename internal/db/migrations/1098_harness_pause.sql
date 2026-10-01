-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- Cooperative pause reuses harness_controls: kind=stop, request_payload.pause=true. The
-- existing kind, phase and forced tenant/project RLS contracts stay intact.
-- Nullable additive columns preserve every existing generation and reader.
ALTER TABLE harness_sessions ADD COLUMN pause_record jsonb;
ALTER TABLE harness_sessions ADD COLUMN continuation_handover jsonb;
