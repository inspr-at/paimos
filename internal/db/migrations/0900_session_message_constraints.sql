-- SPDX-License-Identifier: AGPL-3.0-only
-- Enforce new writes now, but validate historical rows in a later transaction.
ALTER TABLE inbox_messages
 ADD CONSTRAINT inbox_messages_recipient_session_fk FOREIGN KEY (tenant_id,recipient_session_id) REFERENCES harness_sessions(tenant_id,id) NOT VALID,
 ADD CONSTRAINT inbox_messages_sender_session_fk FOREIGN KEY (tenant_id,sender_session_id) REFERENCES harness_sessions(tenant_id,id) NOT VALID;
ALTER TABLE inbox_compat_messages
 ADD CONSTRAINT inbox_compat_recipient_session_fk FOREIGN KEY (tenant_id,project_id,recipient_session_id) REFERENCES harness_sessions(tenant_id,project_id,id) NOT VALID,
 ADD CONSTRAINT inbox_compat_sender_session_fk FOREIGN KEY (tenant_id,project_id,sender_session_id) REFERENCES harness_sessions(tenant_id,project_id,id) NOT VALID;
ALTER TABLE inbox_reply_obligations DROP CONSTRAINT inbox_reply_obligations_check;
ALTER TABLE inbox_reply_obligations
 ADD CONSTRAINT inbox_reply_obligations_closed_reason_check CHECK (closed_reason IN ('session_ended')) NOT VALID,
 ADD CONSTRAINT inbox_reply_obligations_closure CHECK (
  (closed_at IS NULL AND reply_message_id IS NULL AND closed_reason IS NULL) OR
  (closed_at IS NOT NULL AND ((reply_message_id IS NOT NULL AND closed_reason IS NULL) OR (reply_message_id IS NULL AND closed_reason IS NOT NULL)))
 ) NOT VALID;

-- Ending a generation cancels its obligations without inventing a reply.
-- RETURNING emits exactly one event per newly closed obligation, atomically.
CREATE FUNCTION close_session_message_obligations() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.stopped_at IS NULL AND NEW.stopped_at IS NOT NULL) OR (OLD.archived_at IS NULL AND NEW.archived_at IS NOT NULL) THEN
  WITH closed AS (
   UPDATE inbox_reply_obligations o SET closed_at=clock_timestamp(),closed_reason='session_ended'
   FROM inbox_compat_messages m WHERE o.tenant_id=NEW.tenant_id AND m.tenant_id=o.tenant_id AND m.id=o.message_id AND o.closed_at IS NULL
     AND (m.recipient_session_id=NEW.id OR m.sender_session_id=NEW.id)
   RETURNING o.message_id,o.closed_reason
  )
  INSERT INTO events(tenant_id,actor_principal_id,type,after)
  SELECT NEW.tenant_id,aeon_authz_system_actor(NEW.tenant_id),'inbox.reply_obligation_closed',
   jsonb_build_object('message_id',message_id,'closed_reason',closed_reason)
  FROM closed;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER harness_close_message_obligations AFTER UPDATE OF stopped_at,archived_at ON harness_sessions
 FOR EACH ROW EXECUTE FUNCTION close_session_message_obligations();
