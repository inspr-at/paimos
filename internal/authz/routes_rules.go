// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for rules.
func init() {
	registerRoutes("rules", map[string]string{
		"GET /api/rules/budget":                          "rules.read",
		"GET /api/rules/channels":                        "rules.read",
		"GET /api/rules/comparisons":                     "rules.read",
		"GET /api/rules/explained":                       "rules.read",
		"GET /api/rules/layers":                          "rules.read",
		"GET /api/rules/merged":                          "rules.read",
		"GET /api/rules/sets":                            "rules.read",
		"GET /api/rules/sets/{setId}":                    "rules.read",
		"GET /api/rules/sets/{setId}/versions":           "rules.read",
		"GET /api/rules/sets/{setId}/versions/{version}": "rules.read",
		"POST /api/rules/comparisons":                    "rules.write",
		"POST /api/rules/layers":                         "rules.write",
		"POST /api/rules/publish":                        "rules.publish",
		"POST /api/rules/sets":                           "rules.write",
		"POST /api/rules/sets/{setId}/publish":           "rules.publish",
		"POST /api/rules/sets/{setId}/restore":           "rules.publish",
		"PUT /api/rules/budget":                          "settings.manage",
		"PUT /api/rules/sets/{setId}/draft":              "rules.write",
		"PUT /api/rules/sets/{setId}/tldr":               "rules.write",
	})
	registerDeclarations("rules", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"GET /api/rules/budget":                          true,
		"GET /api/rules/channels":                        true,
		"GET /api/rules/comparisons":                     true,
		"GET /api/rules/explained":                       true,
		"GET /api/rules/layers":                          true,
		"GET /api/rules/merged":                          true,
		"GET /api/rules/sets":                            true,
		"GET /api/rules/sets/{setId}":                    true,
		"GET /api/rules/sets/{setId}/versions":           true,
		"GET /api/rules/sets/{setId}/versions/{version}": true,
		"POST /api/rules/comparisons":                    true,
		"POST /api/rules/layers":                         true,
		"POST /api/rules/publish":                        true,
		"POST /api/rules/sets":                           true,
		"POST /api/rules/sets/{setId}/publish":           true,
		"POST /api/rules/sets/{setId}/restore":           true,
		"PUT /api/rules/sets/{setId}/draft":              true,
		"PUT /api/rules/sets/{setId}/tldr":               true,
	})
}
