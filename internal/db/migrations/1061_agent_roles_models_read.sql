-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- The model registry contains no secrets. Add its read permission to custom
-- roles already assigned to agents; retain all existing grants and key scopes.
-- Shared roles receive the same additive baseline. Built-ins are Go templates.
INSERT INTO role_permissions(tenant_id, role_id, permission)
SELECT DISTINCT b.tenant_id, b.role_id, 'models.read'
FROM role_bindings b
JOIN principals p ON p.tenant_id = b.tenant_id AND p.id = b.principal_id
JOIN roles r ON r.tenant_id = b.tenant_id AND r.id = b.role_id
WHERE p.kind = 'agent' AND NOT r.builtin
ON CONFLICT DO NOTHING;
