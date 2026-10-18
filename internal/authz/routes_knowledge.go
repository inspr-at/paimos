// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for knowledge.
func init() {
	registerRoutes("knowledge", map[string]string{
		"DELETE /api/knowledge/{id}":                               "knowledge.delete",
		"GET /api/knowledge":                                       "knowledge.read",
		"GET /api/knowledge/graph":                                 "knowledge.read",
		"GET /api/knowledge/learnings":                             "knowledge.read",
		"GET /api/knowledge/resolve":                               "knowledge.read",
		"GET /api/knowledge/{id}":                                  "knowledge.read",
		"PATCH /api/knowledge/{id}":                                "knowledge.write",
		"POST /api/knowledge":                                      "knowledge.write",
		"POST /api/knowledge/learnings/{learningId}/accept":        "knowledge.write",
		"POST /api/knowledge/learnings/{learningId}/dismiss":       "knowledge.write",
		"POST /api/knowledge/learnings/{learningId}/draft":         "knowledge.write",
		"PUT /api/knowledge/learnings/{learningId}/recommendation": "knowledge.write",
	})
	registerDeclarations("knowledge", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/knowledge":           true,
		"GET /api/knowledge/graph":     true,
		"GET /api/knowledge/learnings": true,
		"GET /api/knowledge/resolve":   true,
	})
	registerDeclarations("knowledge", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/knowledge": true,
	})
}
