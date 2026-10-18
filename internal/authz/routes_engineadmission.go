// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for engineadmission.
func init() {
	registerRoutes("engineadmission", map[string]string{
		"GET /api/projects/{projectId}/admission-settings": "engine.read",
		"POST /api/engine/admission":                       "engine.admission",
		"PUT /api/projects/{projectId}/admission-settings": "engine.manage",
	})
	registerDeclarations("engineadmission", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"POST /api/engine/admission": true,
	})
}
