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

-- Reserve inbox rows before taking the tenant event counter. NULL is allowed
-- only inside a transaction: deferred checks require the final event reference.
-- Existing writers still supply their event ID at INSERT, unchanged.
ALTER TABLE inbox_messages ALTER COLUMN sent_event_id DROP NOT NULL;
ALTER TABLE inbox_compat_messages ALTER COLUMN sent_event_id DROP NOT NULL;
CREATE FUNCTION aeon_inbox_require_sent_event() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE incomplete boolean;
BEGIN
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.%I WHERE tenant_id=$1 AND id=$2 AND sent_event_id IS NULL)', TG_TABLE_SCHEMA, TG_TABLE_NAME)
 INTO incomplete USING NEW.tenant_id, NEW.id;
 IF incomplete THEN
  RAISE EXCEPTION 'inbox message requires a sent event' USING ERRCODE='23502';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER inbox_message_sent_event_required
 AFTER INSERT OR UPDATE ON inbox_messages DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW WHEN (NEW.sent_event_id IS NULL) EXECUTE FUNCTION aeon_inbox_require_sent_event();
CREATE CONSTRAINT TRIGGER inbox_compat_sent_event_required
 AFTER INSERT OR UPDATE ON inbox_compat_messages DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW WHEN (NEW.sent_event_id IS NULL) EXECUTE FUNCTION aeon_inbox_require_sent_event();
