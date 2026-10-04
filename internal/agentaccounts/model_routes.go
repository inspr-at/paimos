// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

// ModelRoleRoutesSQL is the tenant-scoped route relation for current readers.
// Both source tables enforce RLS independently. Security routes are additive:
// previous binaries retain the original table and its fixed role constraint.
const ModelRoleRoutesSQL = `
    SELECT tenant_id, role, priority, profile_id, state, reason, valid_until
    FROM model_role_routes
    UNION ALL
    SELECT tenant_id, role, priority, profile_id, state, reason, valid_until
    FROM model_security_role_routes`
