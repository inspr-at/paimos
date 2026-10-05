-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-596 P1. Expand only: presence of project_delivery is the mode switch.
-- This migration adopts nothing and leaves the journey archive untouched.
SET LOCAL lock_timeout = '5s';

CREATE TABLE project_delivery (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    adopted_at timestamptz NOT NULL DEFAULT now(),
    adopted_by uuid NOT NULL,
    next_sequence integer NOT NULL DEFAULT 1 CHECK (next_sequence >= 1),
    build_defaults jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(build_defaults) = 'object' AND octet_length(build_defaults::text) <= 2048),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    PRIMARY KEY (tenant_id, project_node_id),
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, adopted_by) REFERENCES principals(tenant_id, id)
);
ALTER TABLE project_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_delivery FORCE ROW LEVEL SECURITY;
CREATE POLICY project_delivery_tenant ON project_delivery
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY project_delivery_project_visibility ON project_delivery AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE project_releases (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    release_node_id uuid NOT NULL,
    project_node_id uuid NOT NULL,
    visibility text NOT NULL DEFAULT 'published' CHECK (visibility IN ('published', 'internal')),
    sequence integer CHECK (sequence > 0),
    state text NOT NULL DEFAULT 'planned' CHECK (state IN ('planned', 'building', 'frozen', 'released', 'abandoned')),
    rank text COLLATE "C" NOT NULL CHECK (rank ~ '^[0-9A-Za-z]{0,31}[1-9A-Za-z]$'),
    creation_key text CHECK (length(creation_key) BETWEEN 1 AND 128),
    build_settings jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(build_settings) = 'object' AND octet_length(build_settings::text) <= 2048),
    entry_closes_at timestamptz,
    build_authorized_by uuid,
    build_authorized_at timestamptz,
    version_scheme text CHECK (octet_length(version_scheme) BETWEEN 1 AND 64),
    version text CHECK (octet_length(version) BETWEEN 1 AND 64),
    cut_at timestamptz,
    reservation_basis text CHECK (reservation_basis IN ('history', 'attested')),
    reservation_ref text NOT NULL DEFAULT '' CHECK (length(reservation_ref) <= 512),
    released_by uuid,
    included_in_release_id uuid,
    released_at timestamptz,
    abandoned_at timestamptz,
    origin text NOT NULL DEFAULT 'planned' CHECK (origin IN ('planned', 'adopted_planned', 'adopted_released', 'backfill')),
    bundle_node_id uuid,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    PRIMARY KEY (tenant_id, release_node_id),
    UNIQUE (tenant_id, project_node_id, release_node_id),
    UNIQUE (tenant_id, project_node_id, sequence),
    UNIQUE (tenant_id, project_node_id, rank),
    UNIQUE (tenant_id, project_node_id, creation_key),
    FOREIGN KEY (tenant_id, release_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES project_delivery(tenant_id, project_node_id),
    FOREIGN KEY (tenant_id, build_authorized_by) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, released_by) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, bundle_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, project_node_id, included_in_release_id)
        REFERENCES project_releases(tenant_id, project_node_id, release_node_id),
    CHECK ((visibility = 'published') = (sequence IS NOT NULL)),
    CONSTRAINT project_releases_version_pair CHECK ((version_scheme IS NULL) = (version IS NULL)),
    CHECK (visibility = 'published' OR (version IS NULL AND cut_at IS NULL AND reservation_basis IS NULL AND released_by IS NULL)),
    CONSTRAINT project_releases_build_authorization_pair CHECK ((build_authorized_by IS NULL) = (build_authorized_at IS NULL)),
    CONSTRAINT project_releases_build_authorization_state CHECK (build_authorized_by IS NULL OR state = 'building'),
    CHECK ((state = 'released') = (released_at IS NOT NULL)),
    CHECK ((state = 'abandoned') = (abandoned_at IS NOT NULL)),
    CHECK (cut_at IS NULL OR (version IS NOT NULL AND state IN ('frozen', 'released', 'abandoned'))),
    -- Ordinary cuts assign the whole immutable tuple; adopted history may lack its timestamp.
    CONSTRAINT project_releases_version_cut_pair CHECK (version IS NULL OR cut_at IS NOT NULL OR origin = 'adopted_released'),
    CHECK (reservation_basis IS DISTINCT FROM 'history' OR reservation_ref = ''),
    CONSTRAINT project_releases_publication_proof CHECK (state <> 'released' OR visibility <> 'published' OR origin = 'adopted_released'
        OR (version IS NOT NULL AND cut_at IS NOT NULL AND reservation_basis IS NOT NULL)),
    CHECK (included_in_release_id IS NULL OR (visibility = 'internal' AND state = 'released' AND included_in_release_id <> release_node_id))
);
CREATE UNIQUE INDEX project_releases_version_idx ON project_releases(tenant_id, project_node_id, version)
    WHERE version IS NOT NULL;
CREATE INDEX project_releases_open_idx ON project_releases(tenant_id, project_node_id, visibility, rank)
    WHERE state IN ('planned', 'building', 'frozen');
CREATE INDEX project_releases_pending_internal_idx ON project_releases(tenant_id, project_node_id, released_at, release_node_id)
    WHERE visibility = 'internal' AND state = 'released' AND included_in_release_id IS NULL;
ALTER TABLE project_releases ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_releases FORCE ROW LEVEL SECURITY;
CREATE POLICY project_releases_tenant ON project_releases
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY project_releases_project_visibility ON project_releases AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));

CREATE TABLE ships_in (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    item_node_id uuid NOT NULL,
    project_node_id uuid NOT NULL,
    release_node_id uuid,
    rank text COLLATE "C" NOT NULL CHECK (rank ~ '^[0-9A-Za-z]{0,31}[1-9A-Za-z]$'),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    expedite boolean NOT NULL DEFAULT false,
    due_on date,
    source text NOT NULL CHECK (source IN ('seed', 'adopted', 'person', 'agent', 'build', 'correction', 'backfill')),
    placed_by uuid NOT NULL,
    placed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, item_node_id),
    UNIQUE NULLS NOT DISTINCT (tenant_id, project_node_id, release_node_id, rank),
    FOREIGN KEY (tenant_id, item_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES project_delivery(tenant_id, project_node_id),
    FOREIGN KEY (tenant_id, project_node_id, release_node_id)
        REFERENCES project_releases(tenant_id, project_node_id, release_node_id),
    FOREIGN KEY (tenant_id, placed_by) REFERENCES principals(tenant_id, id)
);
CREATE UNIQUE INDEX ships_in_expedite_idx ON ships_in(tenant_id, project_node_id) WHERE expedite;
ALTER TABLE ships_in ENABLE ROW LEVEL SECURITY;
ALTER TABLE ships_in FORCE ROW LEVEL SECURITY;
CREATE POLICY ships_in_tenant ON ships_in
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY ships_in_project_visibility ON ships_in AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));

-- Keep version as a column: the existing released-outcome function reads it.
CREATE TABLE project_release_note_snapshots (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    release_node_id uuid NOT NULL,
    version text NOT NULL CHECK (octet_length(version) BETWEEN 1 AND 64),
    snapshot jsonb NOT NULL CONSTRAINT project_release_note_snapshots_payload_check CHECK ((
        jsonb_typeof(snapshot) = 'object'
        AND octet_length(snapshot::text) <= 12582912
        AND snapshot->>'schema' = 'aeon.release-note-snapshot.v1'
        AND snapshot->>'membership_source' = 'ships_in.release_node_id'
        AND snapshot->>'field_source' = 'nodes.fields'
        AND snapshot->'frozen' = 'true'::jsonb
        AND snapshot->>'tenant_id' = tenant_id::text
        AND snapshot->>'project_node_id' = project_node_id::text
        AND snapshot->>'release_node_id' = release_node_id::text
        AND snapshot->>'version' = version
        AND jsonb_typeof(snapshot->'tickets') = 'array'
        AND CASE WHEN jsonb_typeof(snapshot->'tickets') = 'array'
            THEN jsonb_array_length(snapshot->'tickets') <= 5000 ELSE false END) IS TRUE),
    PRIMARY KEY (tenant_id, release_node_id),
    FOREIGN KEY (tenant_id, project_node_id, release_node_id)
        REFERENCES project_releases(tenant_id, project_node_id, release_node_id)
);
ALTER TABLE project_release_note_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_release_note_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY project_release_note_snapshots_tenant ON project_release_note_snapshots
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY project_release_note_snapshots_project_visibility ON project_release_note_snapshots AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
