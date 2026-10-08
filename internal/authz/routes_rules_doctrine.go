// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for rules/doctrine.
func init() {
	registerRoutes("rules_doctrine", map[string]string{
		"DELETE /api/rules/doctrine/sources/{sourceId}":            "settings.manage",
		"GET /api/rules/doctrine":                                  "rules.read",
		"GET /api/rules/doctrine/analysis":                         "rules.read",
		"GET /api/rules/doctrine/inbox":                            "rules.read",
		"GET /api/rules/doctrine/inbox/summary":                    "rules.read",
		"GET /api/rules/doctrine/proposals":                        "rules.read",
		"POST /api/rules/doctrine/inbox":                           "rules.write",
		"POST /api/rules/doctrine/inbox/{proposalId}/dismiss":      "rules.write",
		"POST /api/rules/doctrine/inbox/{proposalId}/notified":     "rules.read",
		"POST /api/rules/doctrine/inbox/{proposalId}/pull-request": "rules.write",
		"POST /api/rules/doctrine/proposals":                       "rules.write",
		"POST /api/rules/doctrine/proposals/{proposalId}/approve":  "rules.publish",
		"POST /api/rules/doctrine/proposals/{proposalId}/pins":     "settings.manage",
		"POST /api/rules/doctrine/proposals/{proposalId}/refresh":  "rules.write",
		"POST /api/rules/doctrine/sources":                         "settings.manage",
		"POST /api/rules/doctrine/sources/{sourceId}/index":        "settings.manage",
		"PUT /api/rules/doctrine/sources/{sourceId}":               "settings.manage",
	})
}
