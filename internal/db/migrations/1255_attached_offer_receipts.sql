-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
ALTER TABLE attached_message_grants ADD COLUMN hook_epoch text;

ALTER TABLE harness_deliveries
 ADD COLUMN mode text NOT NULL DEFAULT 'managed' CHECK(mode IN ('managed','attached_hook')),
 ADD COLUMN message_grant_id uuid,
 ADD COLUMN message_generation uuid,
 ADD COLUMN daemon_epoch text,
 ADD COLUMN nonce_digest text,
 ADD COLUMN hook_epoch text,
 ADD COLUMN offer_deadline timestamptz,
 ADD COLUMN body_released_at timestamptz,
 ADD COLUMN shown_at timestamptz,
 ADD COLUMN terminal_outcome text CHECK(terminal_outcome IN ('completed','uncertain','revoked','expired','not_delivered','cancelled')),
 ADD COLUMN receipt_outcome text CHECK(receipt_outcome IN ('shown','uncertain')),
 ADD FOREIGN KEY(tenant_id,message_grant_id) REFERENCES attached_message_grants(tenant_id,id),
 ADD CONSTRAINT attached_offer_metadata CHECK(mode='managed' OR
  (message_grant_id IS NOT NULL AND message_generation IS NOT NULL AND daemon_epoch ~ '^[a-f0-9]{64}$'
   AND hook_epoch IS NOT NULL AND length(hook_epoch) BETWEEN 1 AND 128
   AND nonce_digest ~ '^[a-f0-9]{64}$' AND offer_deadline IS NOT NULL) IS TRUE);
CREATE UNIQUE INDEX attached_one_attempt ON harness_deliveries(tenant_id,message_id) WHERE mode='attached_hook';
-- Backstop against old managed clients importing an attached message or resetting
-- a lease. The existing row is the attempt: it can never be replaced or revived.
CREATE FUNCTION aeon_attached_delivery_fence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE m inbox_messages;
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.mode='attached_hook' THEN RAISE EXCEPTION 'attached attempt is immutable' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 SELECT * INTO m FROM inbox_messages WHERE tenant_id=NEW.tenant_id AND id=NEW.message_id;
 IF NEW.mode='attached_hook' THEN
  IF (m.content_mode='attached_volatile' AND m.recipient_session_id=NEW.session_id
      AND m.message_grant_id=NEW.message_grant_id AND m.recipient_message_generation=NEW.message_generation) IS NOT TRUE THEN
   RAISE EXCEPTION 'attached delivery binding mismatch' USING ERRCODE='23514';
  END IF;
 ELSIF m.content_mode<>'durable' THEN
  RAISE EXCEPTION 'attached message cannot enter managed drain' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' AND OLD.mode='attached_hook' AND
  (ROW(NEW.mode,NEW.tenant_id,NEW.id,NEW.session_id,NEW.message_id,NEW.message_grant_id,NEW.message_generation,NEW.daemon_epoch,NEW.nonce_digest,NEW.hook_epoch,NEW.leased_at,NEW.offer_deadline)
   IS DISTINCT FROM ROW(OLD.mode,OLD.tenant_id,OLD.id,OLD.session_id,OLD.message_id,OLD.message_grant_id,OLD.message_generation,OLD.daemon_epoch,OLD.nonce_digest,OLD.hook_epoch,OLD.leased_at,OLD.offer_deadline)
   OR (OLD.body_released_at IS NOT NULL AND NEW.body_released_at IS DISTINCT FROM OLD.body_released_at)
   OR (OLD.terminal_outcome IS NOT NULL AND NEW IS DISTINCT FROM OLD)) THEN
  RAISE EXCEPTION 'attached attempt cannot replay' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER attached_delivery_fence BEFORE INSERT OR UPDATE OR DELETE ON harness_deliveries
 FOR EACH ROW EXECUTE FUNCTION aeon_attached_delivery_fence();
-- Add revoked to the existing content-free outcome contract.
DO $$
DECLARE tbl text;
BEGIN
 FOREACH tbl IN ARRAY ARRAY['inbox_messages','inbox_compat_messages'] LOOP
  EXECUTE format('ALTER TABLE %I DROP CONSTRAINT attached_content_free',tbl);
  EXECUTE format('ALTER TABLE %I ADD CONSTRAINT attached_content_free CHECK((content_mode=''durable'' OR (body=''Attached-session note; text not retained'' AND recipient_session_id IS NOT NULL AND message_deadline IS NOT NULL AND payload_bytes BETWEEN 1 AND 4096 AND (content_mode=''attached_notification'' OR (message_grant_id IS NOT NULL AND recipient_message_generation IS NOT NULL AND payload_epoch IS NOT NULL)) AND attached_outcome IN (''queued'',''notification_only'',''offered'',''shown'',''completed'',''not_delivered'',''cancelled'',''expired'',''uncertain'',''revoked''))) IS TRUE)',tbl);
 END LOOP;
END $$;
