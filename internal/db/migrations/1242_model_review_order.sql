-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Sparse ordering metadata. Absence means legacy, including for new tenants.
-- Only an authorized conditional review-gate replacement may activate saved.
CREATE TABLE model_review_order (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    ordinary_review_order_mode text NOT NULL DEFAULT 'legacy'
        CHECK (ordinary_review_order_mode IN ('legacy', 'saved'))
);
ALTER TABLE model_review_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_review_order FORCE ROW LEVEL SECURITY;
CREATE POLICY model_review_order_tenant ON model_review_order
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
