-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-318: the doctrine as a git-backed, read-only rule layer. Git is the
-- source of truth. doctrine_sources is tenant configuration: which repository
-- is indexed at which pinned commit, and the NAME of a read-only credential
-- the server resolves at fetch time (never the credential itself).
-- doctrine_cache holds the exact file bytes at a source's pinned commit and
-- nothing else: moving the pin deletes every row of the old commit, so Aeon
-- keeps no copy of the doctrine beyond this commit-keyed cache.
SET LOCAL lock_timeout = '5s';

CREATE TABLE doctrine_sources (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    repository text NOT NULL CHECK (repository ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$'),
    visibility text NOT NULL CHECK (visibility IN ('public', 'private')),
    ref text NOT NULL DEFAULT '' CHECK (ref = '' OR (octet_length(ref) <= 128 AND ref ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$' AND ref !~ '\.\.')),
    commit_sha text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    committed_at timestamptz,
    pinned_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    paths text[] NOT NULL CHECK (cardinality(paths) BETWEEN 1 AND 20),
    credential_ref text NOT NULL DEFAULT '' CHECK (credential_ref = '' OR credential_ref ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    indexed_at timestamptz,
    index_error text NOT NULL DEFAULT '' CHECK (octet_length(index_error) <= 300 AND index_error !~ '[[:cntrl:]]'),
    skipped jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(skipped) = 'array' AND octet_length(skipped::text) <= 16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, repository),
    CONSTRAINT doctrine_private_needs_credential CHECK (visibility = 'public' OR credential_ref <> '')
);
ALTER TABLE doctrine_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_sources_tenant ON doctrine_sources
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE TABLE doctrine_cache (
    tenant_id uuid NOT NULL,
    source_id uuid NOT NULL,
    commit_sha text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    path text NOT NULL CHECK (octet_length(path) BETWEEN 1 AND 300),
    blob_sha text NOT NULL CHECK (blob_sha ~ '^[0-9a-f]{40}$'),
    content bytea NOT NULL CHECK (octet_length(content) <= 262144),
    fetched_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, source_id, commit_sha, path),
    FOREIGN KEY (tenant_id, source_id) REFERENCES doctrine_sources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE doctrine_cache ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_cache FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_cache_tenant ON doctrine_cache
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- The cache holds the pinned commit only. An insert for any other commit
-- fails, and moving a pin deletes the rows of the commit it left. FOR SHARE
-- orders an insert against a concurrent pin change on the same source row.
CREATE FUNCTION aeon_doctrine_cache_pinned() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM doctrine_sources s
        WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.source_id AND s.commit_sha = NEW.commit_sha
        FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'doctrine cache holds the pinned commit only';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER doctrine_cache_pinned BEFORE INSERT OR UPDATE ON doctrine_cache
    FOR EACH ROW EXECUTE FUNCTION aeon_doctrine_cache_pinned();

CREATE FUNCTION aeon_doctrine_unpin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM doctrine_cache
        WHERE tenant_id = NEW.tenant_id AND source_id = NEW.id AND commit_sha <> NEW.commit_sha;
    RETURN NULL;
END;
$$;
CREATE TRIGGER doctrine_sources_unpin AFTER UPDATE OF commit_sha ON doctrine_sources
    FOR EACH ROW WHEN (OLD.commit_sha IS DISTINCT FROM NEW.commit_sha)
    EXECUTE FUNCTION aeon_doctrine_unpin();
