-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1330: additive preparation metadata; older writers remain valid.
SET LOCAL lock_timeout = '5s';
ALTER TABLE method_learning_recommendations ADD COLUMN source_summary text;
ALTER TABLE method_learning_recommendations ADD COLUMN changelog_line text;
ALTER TABLE method_learning_recommendations ADD COLUMN rule_layer_id uuid;
ALTER TABLE method_learning_nominations ADD COLUMN signal text;

-- The migration timestamp is the release cutoff, not the time of each restart.
-- Per-tenant keyset progress survives interrupted runs; completion survives undo.
CREATE TABLE method_learning_legacy_cleanup (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    cutoff timestamptz NOT NULL DEFAULT clock_timestamp(),
    after_key text,
    completed_at timestamptz
);
INSERT INTO method_learning_legacy_cleanup (tenant_id) SELECT id FROM tenants;

ALTER TABLE method_learning_legacy_cleanup ENABLE ROW LEVEL SECURITY;
ALTER TABLE method_learning_legacy_cleanup FORCE ROW LEVEL SECURITY;
CREATE POLICY method_learning_legacy_cleanup_tenant ON method_learning_legacy_cleanup
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

