// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for outcomes.
func init() {
	registerRoutes("outcomes", map[string]string{
		"GET /api/nodes/{nodeId}/escalation":                   "outcome.read",
		"GET /api/outcomes":                                    "outcome.read",
		"GET /api/outcomes/measurement":                        "outcome.read",
		"POST /api/outcomes":                                   "outcome.write",
		"POST /api/outcomes/review-rounds":                     "outcome.write",
		"GET /api/projects/{projectId}/delivery/review-rounds": "outcome.read",
	})
	registerDeclarations("outcomes", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/outcomes":             true,
		"GET /api/outcomes/measurement": true,
	})
	registerDeclarations("outcomes", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/outcomes":               true,
		"POST /api/outcomes/review-rounds": true,
	})
}
