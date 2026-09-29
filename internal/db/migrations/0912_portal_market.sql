-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. Market comparison and delivery pace for the public product portal.
-- Cells stay unknown until a person approves a sourced fact. Pace is a count
-- and two medians for one project: no ticket title, person or private field.
-- The node row guard is unchanged. These tables are not portal node kinds.
SET LOCAL lock_timeout = '5s';

CREATE TABLE portal_competitors (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    product_id uuid NOT NULL,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 80 AND name !~ '[[:cntrl:]]'),
    position integer NOT NULL CHECK (position > 0),
    published boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, product_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX portal_competitors_name_idx ON portal_competitors (tenant_id, product_id, lower(name));
CREATE INDEX portal_competitors_product_idx ON portal_competitors (tenant_id, product_id, position, id);
ALTER TABLE portal_competitors ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_competitors FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_competitors_tenant ON portal_competitors
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_competitors IS 'Named competitors for one portal product. Unpublished names stay off the public page.';

CREATE TABLE portal_aspects (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    product_id uuid NOT NULL,
    label text NOT NULL CHECK (label = btrim(label) AND length(label) BETWEEN 1 AND 120 AND label !~ '[[:cntrl:]]'),
    position integer NOT NULL CHECK (position > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, product_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX portal_aspects_label_idx ON portal_aspects (tenant_id, product_id, lower(label));
CREATE INDEX portal_aspects_product_idx ON portal_aspects (tenant_id, product_id, position, id);
ALTER TABLE portal_aspects ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_aspects FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_aspects_tenant ON portal_aspects
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_aspects IS 'Rows of the public comparison. A label is not a claim.';

CREATE TABLE portal_cells (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    aspect_id uuid NOT NULL,
    competitor_id uuid NOT NULL,
    stance text NOT NULL DEFAULT 'unknown' CHECK (stance IN ('unknown', 'yes', 'no', 'partial')),
    quote text NOT NULL DEFAULT '' CHECK (length(quote) <= 400 AND quote !~ '[[:cntrl:]]'),
    source_url text NOT NULL DEFAULT '' CHECK (length(source_url) <= 500 AND (source_url = '' OR source_url ~ '^https://[^[:space:]]+$')),
    retrieved_on date,
    approved boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, aspect_id, competitor_id),
    FOREIGN KEY (tenant_id, aspect_id) REFERENCES portal_aspects(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, competitor_id) REFERENCES portal_competitors(tenant_id, id) ON DELETE CASCADE,
    CHECK (approved = false OR stance = 'unknown' OR (length(btrim(quote)) >= 8 AND source_url ~ '^https://' AND retrieved_on IS NOT NULL))
);
ALTER TABLE portal_cells ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_cells FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_cells_tenant ON portal_cells
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_cells IS 'One comparison fact. Unapproved cells are unknown on the public page.';

CREATE TABLE portal_cell_revisions (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    cell_id uuid NOT NULL,
    stance text NOT NULL CHECK (stance IN ('unknown', 'yes', 'no', 'partial')),
    quote text NOT NULL DEFAULT '' CHECK (length(quote) <= 400),
    source_url text NOT NULL DEFAULT '' CHECK (length(source_url) <= 500),
    retrieved_on date,
    approved boolean NOT NULL,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, cell_id) REFERENCES portal_cells(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX portal_cell_revisions_cell_idx ON portal_cell_revisions (tenant_id, cell_id, at DESC);
ALTER TABLE portal_cell_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_cell_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_cell_revisions_tenant ON portal_cell_revisions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_cell_revisions IS 'Append-only history of a comparison cell. Admin only.';

CREATE TABLE portal_corrections (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    product_id uuid NOT NULL,
    competitor_name text NOT NULL CHECK (length(competitor_name) BETWEEN 1 AND 80),
    aspect_label text NOT NULL CHECK (length(aspect_label) BETWEEN 1 AND 120),
    statement text NOT NULL CHECK (length(statement) BETWEEN 1 AND 2000),
    source_url text NOT NULL DEFAULT '' CHECK (length(source_url) <= 500),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'closed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, product_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX portal_corrections_pending_idx ON portal_corrections (tenant_id, product_id, created_at) WHERE state = 'pending';
ALTER TABLE portal_corrections ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_corrections FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_corrections_tenant ON portal_corrections
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_corrections IS 'Anonymous factual corrections. No name, email or address column.';

CREATE TABLE portal_pace (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE portal_pace ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_pace FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_pace_tenant ON portal_pace
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_pace IS 'The one project whose released dates become the public release count. The project name is not public.';

CREATE TABLE portal_fulfillments (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    wish_id uuid NOT NULL,
    feature_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, wish_id),
    FOREIGN KEY (tenant_id, wish_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, feature_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE,
    CHECK (wish_id <> feature_id)
);
ALTER TABLE portal_fulfillments ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_fulfillments FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_fulfillments_tenant ON portal_fulfillments
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
COMMENT ON TABLE portal_fulfillments IS 'Links a wish to the live feature that fulfilled it. The public figure is a median of days.';
