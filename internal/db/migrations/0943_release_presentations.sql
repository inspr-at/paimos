-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-305: how a release introduces itself. One row per tenant, product
-- project and calendar version: a short theme (the kicker), one headline
-- sentence and a short intro, each in English and German. Additive: it never
-- touches tags, manifests or note snapshots. Every change also writes an event.
SET LOCAL lock_timeout = '5s';

CREATE TABLE release_presentations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    version text NOT NULL CHECK (version ~ '^[1-9][0-9](0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])([01][0-9]|2[0-3])[0-5][0-9][0-5][0-9]\.0\.0$'),
    theme_en text NOT NULL CHECK (char_length(btrim(theme_en)) BETWEEN 1 AND 80),
    theme_de text NOT NULL DEFAULT '' CHECK (char_length(theme_de) <= 80),
    headline_en text NOT NULL CHECK (char_length(btrim(headline_en)) BETWEEN 1 AND 200),
    headline_de text NOT NULL DEFAULT '' CHECK (char_length(headline_de) <= 200),
    intro_en text NOT NULL DEFAULT '' CHECK (char_length(intro_en) <= 600),
    intro_de text NOT NULL DEFAULT '' CHECK (char_length(intro_de) <= 600),
    revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_node_id, version),
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, updated_by) REFERENCES principals(tenant_id, id)
);
ALTER TABLE release_presentations ENABLE ROW LEVEL SECURITY;
ALTER TABLE release_presentations FORCE ROW LEVEL SECURITY;
CREATE POLICY release_presentations_tenant ON release_presentations
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY release_presentations_project_visibility ON release_presentations AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
