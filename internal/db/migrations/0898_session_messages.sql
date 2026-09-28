-- SPDX-License-Identifier: AGPL-3.0-only
-- Old rows remain unbound: a principal's current generation cannot own history.
ALTER TABLE inbox_messages
 ADD COLUMN recipient_session_id uuid,
 ADD COLUMN sender_session_id uuid,
 ADD COLUMN sender_label text,
 ADD FOREIGN KEY (tenant_id,recipient_session_id) REFERENCES harness_sessions(tenant_id,id),
 ADD FOREIGN KEY (tenant_id,sender_session_id) REFERENCES harness_sessions(tenant_id,id);
ALTER TABLE inbox_compat_messages
 ADD COLUMN recipient_session_id uuid,
 ADD COLUMN sender_session_id uuid,
 ADD COLUMN sender_label text,
 ADD FOREIGN KEY (tenant_id,project_id,recipient_session_id) REFERENCES harness_sessions(tenant_id,project_id,id),
 ADD FOREIGN KEY (tenant_id,project_id,sender_session_id) REFERENCES harness_sessions(tenant_id,project_id,id);
CREATE INDEX inbox_session_pending ON inbox_messages(tenant_id,recipient_session_id,sent_event_id) WHERE recipient_session_id IS NOT NULL AND acked_at IS NULL;
CREATE INDEX inbox_compat_session ON inbox_compat_messages(tenant_id,project_id,recipient_session_id,sent_event_id) WHERE recipient_session_id IS NOT NULL;

-- Ending a generation cancels its obligations without inventing a reply.
ALTER TABLE inbox_reply_obligations DROP CONSTRAINT inbox_reply_obligations_check;
ALTER TABLE inbox_reply_obligations ADD COLUMN closed_reason text CHECK (closed_reason IN ('session_ended'));
ALTER TABLE inbox_reply_obligations ADD CONSTRAINT inbox_reply_obligations_closure CHECK (
 (closed_at IS NULL AND reply_message_id IS NULL AND closed_reason IS NULL) OR
 (closed_at IS NOT NULL AND ((reply_message_id IS NOT NULL AND closed_reason IS NULL) OR (reply_message_id IS NULL AND closed_reason IS NOT NULL)))
);
CREATE FUNCTION close_session_message_obligations() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.stopped_at IS NULL AND NEW.stopped_at IS NOT NULL) OR (OLD.archived_at IS NULL AND NEW.archived_at IS NOT NULL) THEN
  UPDATE inbox_reply_obligations o SET closed_at=clock_timestamp(),closed_reason='session_ended'
  FROM inbox_compat_messages m WHERE o.tenant_id=NEW.tenant_id AND m.tenant_id=o.tenant_id AND m.id=o.message_id AND o.closed_at IS NULL
    AND (m.recipient_session_id=NEW.id OR m.sender_session_id=NEW.id);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER harness_close_message_obligations AFTER UPDATE OF stopped_at,archived_at ON harness_sessions
 FOR EACH ROW EXECUTE FUNCTION close_session_message_obligations();
