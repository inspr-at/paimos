-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-319: quotation guard for public doctrine proposals. The row stores
-- hashes of normalised private spans at the pinned commit, never the text.
SET LOCAL lock_timeout = '5s';

CREATE TABLE doctrine_private_guard (
    tenant_id uuid NOT NULL,
    source_id uuid NOT NULL,
    commit_sha text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    corpus bytea NOT NULL CHECK (octet_length(corpus) BETWEEN 4 AND 8388608),
    PRIMARY KEY (tenant_id, source_id),
    FOREIGN KEY (tenant_id, source_id) REFERENCES doctrine_sources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE doctrine_private_guard ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_private_guard FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_private_guard_tenant ON doctrine_private_guard
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE FUNCTION aeon_doctrine_guard_pinned() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM doctrine_sources s
        WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.source_id AND s.commit_sha = NEW.commit_sha
        FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'doctrine guard holds the pinned commit only';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER doctrine_private_guard_pinned BEFORE INSERT OR UPDATE ON doctrine_private_guard
    FOR EACH ROW EXECUTE FUNCTION aeon_doctrine_guard_pinned();

CREATE OR REPLACE FUNCTION aeon_doctrine_unpin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM doctrine_cache
        WHERE tenant_id = NEW.tenant_id AND source_id = NEW.id AND commit_sha <> NEW.commit_sha;
    DELETE FROM doctrine_private_guard
        WHERE tenant_id = NEW.tenant_id AND source_id = NEW.id AND commit_sha <> NEW.commit_sha;
    RETURN NULL;
END;
$$;
