// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for authz.
func init() {
	registerRoutes("authz", map[string]string{
		"DELETE /api/members/invites/{id}":                               "members.manage",
		"DELETE /api/members/{principal_id}/aliases/{from_principal_id}": "members.manage",
		"DELETE /api/projects/{projectId}/members/{principal_id}":        "members.manage",
		"DELETE /api/roles/{id}":                                         "roles.manage",
		"GET /api/audit":                                                 "audit.read",
		"GET /api/authz/permissions":                                     "roles.read",
		"GET /api/me/permissions":                                        "authz.read",
		"GET /api/members":                                               "members.read",
		"GET /api/projects/{projectId}/members":                          "members.read",
		"GET /api/roles":                                                 "roles.read",
		"PATCH /api/roles/{id}":                                          "roles.manage",
		"POST /api/members/agents":                                       "keys.manage",
		"POST /api/members/invites":                                      "members.manage",
		"POST /api/members/invites/{id}/provision":                       "members.manage",
		"POST /api/members/{principal_id}/aliases":                       "members.manage",
		"POST /api/members/{principal_id}/deactivate":                    "members.manage",
		"POST /api/members/{principal_id}/reactivate":                    "members.manage",
		"POST /api/roles":                                                "roles.manage",
		"PUT /api/members/{principal_id}/workspace-role":                 "members.manage",
		"PUT /api/projects/{projectId}/members/{principal_id}":           "members.manage",
	})
	registerDeclarations("authz", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/me/permissions": true,
	})
}
