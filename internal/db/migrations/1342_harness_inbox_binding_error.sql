-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only: older heartbeat writers leave this optional diagnostic alone.
ALTER TABLE harness_sessions ADD COLUMN inbox_binding_error text;
