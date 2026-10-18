-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-565: immutable delayed-application inputs and exact effect provenance.
SET LOCAL lock_timeout = '5s';
ALTER TABLE desk_answers ADD COLUMN outcome_data jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK (jsonb_typeof(outcome_data)='object' AND octet_length(outcome_data::text)<=4096);
ALTER TABLE desk_pending ADD COLUMN effect_data jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK (jsonb_typeof(effect_data)='object' AND octet_length(effect_data::text)<=16384);
ALTER TABLE desk_pending ADD COLUMN error_message text NOT NULL DEFAULT ''
 CHECK (octet_length(error_message)<=1000);
CREATE INDEX desk_outcome_retry ON desk_pending(tenant_id,retry_at,deliver_after,id)
 WHERE state IN ('pending','failed') AND kind='outcome';
