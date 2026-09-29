-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-251: one-time harness comparison summaries. Hashes, statuses and counts
-- only. Instruction bodies, absolute paths and rule prose are not columns.
SET LOCAL lock_timeout = '5s';

CREATE TABLE rules_comparisons (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL,
    person_id uuid NOT NULL,
    agent_id uuid,
    harness text NOT NULL CHECK (harness IN ('claude-code', 'codex')),
    role text NOT NULL CHECK (role IN ('coordinator', 'builder', 'reviewer', 'operator')),
    repo_sha256 text NOT NULL CHECK (repo_sha256 ~ '^[0-9a-f]{64}$'),
    repo_name text NOT NULL DEFAULT '' CHECK (repo_name = '' OR repo_name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    merge_sha256 text NOT NULL CHECK (merge_sha256 ~ '^[0-9a-f]{64}$'),
    merge_version text NOT NULL CHECK (octet_length(merge_version) BETWEEN 1 AND 128 AND merge_version !~ '[[:cntrl:]]'),
    local_set_sha256 text NOT NULL CHECK (local_set_sha256 ~ '^[0-9a-f]{64}$'),
    counts jsonb NOT NULL CHECK (jsonb_typeof(counts) = 'object' AND octet_length(counts::text) <= 512),
    rules jsonb NOT NULL CHECK (jsonb_typeof(rules) = 'array' AND jsonb_array_length(rules) <= 500 AND octet_length(rules::text) <= 262144),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, agent_id) REFERENCES principals(tenant_id, id)
);
CREATE INDEX rules_comparisons_latest ON rules_comparisons (tenant_id, project_id, harness, created_at DESC);
ALTER TABLE rules_comparisons ENABLE ROW LEVEL SECURITY;
ALTER TABLE rules_comparisons FORCE ROW LEVEL SECURITY;
CREATE POLICY rules_comparisons_tenant ON rules_comparisons
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY rules_comparisons_project_visibility ON rules_comparisons AS RESTRICTIVE
    USING (aeon_visible_all() OR project_id = ANY (aeon_visible_projects()))
    WITH CHECK (aeon_visible_all() OR project_id = ANY (aeon_visible_projects()));
CREATE TRIGGER rules_comparisons_immutable
    BEFORE UPDATE OR DELETE ON rules_comparisons
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();
