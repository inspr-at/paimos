// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for questions.
func init() {
	registerRoutes("questions", map[string]string{
		"GET /api/decision-desk":                    "questions.read",
		"GET /api/projects/{projectId}/questions":   "questions.read",
		"GET /api/questions/{questionId}":           "questions.read",
		"GET /api/questions/{questionId}/status":    "questions.read",
		"POST /api/projects/{projectId}/questions":  "questions.ask",
		"POST /api/questions/{questionId}/decision": "questions.decide",
	})
	registerDeclarations("questions", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/decision-desk": true,
	})
}
