-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only: older writers leave both columns null.
ALTER TABLE principals ADD COLUMN messaging_only boolean;
ALTER TABLE principals ADD COLUMN messaging_owner_id uuid;
CREATE INDEX principals_messaging_owner_idx ON principals (tenant_id, messaging_owner_id)
    WHERE messaging_only IS TRUE;
