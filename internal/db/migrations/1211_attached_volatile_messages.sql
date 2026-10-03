-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
DO $$
DECLARE tbl text;
BEGIN
 FOREACH tbl IN ARRAY ARRAY['inbox_messages','inbox_compat_messages'] LOOP
  EXECUTE format('ALTER TABLE %I ADD COLUMN content_mode text NOT NULL DEFAULT ''durable'' CHECK(content_mode IN (''durable'',''attached_volatile'',''attached_notification'')), ADD COLUMN message_grant_id uuid, ADD COLUMN recipient_message_generation uuid, ADD COLUMN message_deadline timestamptz, ADD COLUMN payload_bytes integer NOT NULL DEFAULT 0, ADD COLUMN payload_epoch uuid, ADD COLUMN attached_outcome text',tbl);
  EXECUTE format('ALTER TABLE %I ADD FOREIGN KEY(tenant_id,message_grant_id) REFERENCES attached_message_grants(tenant_id,id)',tbl);
  EXECUTE format('ALTER TABLE %I ADD CONSTRAINT attached_content_free CHECK((content_mode=''durable'' OR (body=''Attached-session note; text not retained'' AND recipient_session_id IS NOT NULL AND message_deadline IS NOT NULL AND payload_bytes BETWEEN 1 AND 4096 AND (content_mode=''attached_notification'' OR (message_grant_id IS NOT NULL AND recipient_message_generation IS NOT NULL AND payload_epoch IS NOT NULL)) AND attached_outcome IN (''queued'',''notification_only'',''offered'',''shown'',''completed'',''not_delivered'',''cancelled'',''expired'',''uncertain''))) IS TRUE)',tbl);
 END LOOP;
END $$;
ALTER TABLE inbox_compat_messages ADD CONSTRAINT attached_safe_mode CHECK(content_mode='durable' OR (NOT is_action_request AND NOT expects_reply AND delivery_level='simple' AND request_digest=repeat('0',64)));
-- Defense in depth: legacy ACK/fetch consumers cannot advance attached authority.
CREATE FUNCTION aeon_attached_no_legacy_ack() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.content_mode<>'durable' AND (NEW.acked_at IS DISTINCT FROM OLD.acked_at OR NEW.fetched_at IS DISTINCT FROM OLD.fetched_at) THEN
  RAISE EXCEPTION 'attached notes require the attached receipt protocol' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER attached_no_legacy_ack BEFORE UPDATE ON inbox_messages FOR EACH ROW EXECUTE FUNCTION aeon_attached_no_legacy_ack();
CREATE INDEX attached_pending_notes ON inbox_messages(tenant_id,recipient_session_id,message_deadline) WHERE content_mode<>'durable' AND attached_outcome IN ('queued','offered');
