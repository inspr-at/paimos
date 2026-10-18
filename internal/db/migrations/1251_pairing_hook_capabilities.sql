-- SPDX-License-Identifier: AGPL-3.0-only
-- Content-free capability projection on the existing tenant/RLS computer row.
-- No consent, message grant, payload or second delivery queue is created.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_pairing_computers ADD COLUMN hook_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(hook_capabilities) = 'array' AND jsonb_array_length(hook_capabilities) <= 5);
