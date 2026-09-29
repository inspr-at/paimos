-- SPDX-License-Identifier: AGPL-3.0-only
-- Optional display/audit metadata. Existing requests and grants remain valid.
SET LOCAL lock_timeout = '5s';
ALTER TABLE approval_requests ADD COLUMN target jsonb;
ALTER TABLE approval_requests ADD COLUMN target_digest_sha256 text
    CHECK (target_digest_sha256 ~ '^[0-9a-f]{64}$');
ALTER TABLE approval_requests ADD CONSTRAINT approval_target_pair
    CHECK ((target IS NULL) = (target_digest_sha256 IS NULL));
ALTER TABLE stage_handoffs ADD COLUMN target jsonb;
ALTER TABLE stage_handoffs ADD COLUMN target_digest_sha256 text
    CHECK (target_digest_sha256 ~ '^[0-9a-f]{64}$');
ALTER TABLE stage_handoffs ADD CONSTRAINT handoff_target_pair
    CHECK ((target IS NULL) = (target_digest_sha256 IS NULL));
CREATE FUNCTION aeon_guard_handoff_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.target IS DISTINCT FROM OLD.target OR
       NEW.target_digest_sha256 IS DISTINCT FROM OLD.target_digest_sha256 THEN
        RAISE EXCEPTION 'stage handoff target is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stage_handoff_target_immutable BEFORE UPDATE ON stage_handoffs
    FOR EACH ROW EXECUTE FUNCTION aeon_guard_handoff_target();
