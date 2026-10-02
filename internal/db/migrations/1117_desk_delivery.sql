-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-564: additive delivery scheduling and immutable correction lineage.
SET LOCAL lock_timeout = '5s';
ALTER TABLE desk_answers ADD COLUMN replaces uuid,
 ADD CONSTRAINT desk_answer_replaces_fk
 FOREIGN KEY (tenant_id,project_id,replaces) REFERENCES desk_answers(tenant_id,project_id,node_id);
ALTER TABLE desk_pending ADD COLUMN retry_at timestamptz;
ALTER TABLE desk_pending ADD COLUMN delivery_session_id uuid,
 ADD CONSTRAINT desk_delivery_session_fk
 FOREIGN KEY (tenant_id,project_id,delivery_session_id) REFERENCES harness_sessions(tenant_id,project_id,id);
-- Durable answers survive an ended generation even without a pause record.
ALTER TABLE harness_sessions ADD COLUMN desk_answers jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_desk_answers_array
 CHECK (jsonb_typeof(desk_answers)='array' AND jsonb_array_length(desk_answers)<=100) NOT VALID;
CREATE INDEX desk_delivery_retry ON desk_pending(tenant_id,retry_at,deliver_after,id)
 WHERE state IN ('pending','failed') AND kind IN ('inbox','comment');
