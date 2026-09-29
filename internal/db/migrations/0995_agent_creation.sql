-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicitly configured agents must never receive an automatic workspace role
-- when their first key is issued (including after their bindings are removed).
ALTER TABLE principals ADD COLUMN description text NOT NULL DEFAULT '';
ALTER TABLE principals ADD COLUMN agent_access_configured boolean NOT NULL DEFAULT false;
