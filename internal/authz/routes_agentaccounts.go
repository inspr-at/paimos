// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for agentaccounts.
func init() {
	registerRoutes("agentaccounts", map[string]string{
		"DELETE /api/agent-accounts/groups/{id}":                         "account.manage",
		"DELETE /api/agent-accounts/pins":                                "run.create",
		"DELETE /api/agent-accounts/{accountId}/windows/{windowId}":      "account.manage",
		"DELETE /api/agent-accounts/{accountId}/{resource}":              "account.manage",
		"GET /api/agent-accounts":                                        "account.read",
		"GET /api/agent-accounts/capacity":                               "account.read",
		"GET /api/agent-accounts/capacity/next":                          "account.read|account.probe",
		"GET /api/agent-accounts/capacity/schedule":                      "account.read",
		"GET /api/agent-accounts/catalog":                                "account.read",
		"GET /api/agent-accounts/groups":                                 "account.read",
		"GET /api/agent-accounts/overview":                               "account.read|account.overview.read",
		"GET /api/agent-accounts/pins":                                   "account.read",
		"GET /api/agent-accounts/posture":                                "account.read",
		"GET /api/agent-accounts/quota-warnings":                         "account.read",
		"GET /api/agent-accounts/readiness":                              "account.read",
		"GET /api/agent-accounts/use":                                    "account.read|account.probe",
		"GET /api/agent-accounts/{accountId}/readings":                   "account.read|account.probe", // Handler requires read for people, probe + ownership for agents.
		"GET /api/agent-accounts/{accountId}/residency-evidence":         "account.read",
		"GET /api/agent-accounts/{accountId}/statusline":                 "account.probe",
		"GET /api/settings/quota-warnings":                               "account.read",
		"PATCH /api/agent-accounts/groups/{id}":                          "account.manage",
		"PATCH /api/agent-accounts/{accountId}":                          "account.manage",
		"POST /api/agent-accounts":                                       "account.manage",
		"POST /api/agent-accounts/capacity/preview":                      "account.read",
		"POST /api/agent-accounts/groups":                                "account.manage",
		"POST /api/agent-accounts/route":                                 "account.route",
		"POST /api/agent-accounts/runs/{runId}/target":                   "run.create",
		"POST /api/agent-accounts/{accountId}/archive":                   "account.manage", // Person-only Remove (AEON-402).
		"POST /api/agent-accounts/{accountId}/capacity/approve":          "account.manage",
		"POST /api/agent-accounts/{accountId}/check":                     "account.manage",
		"POST /api/agent-accounts/{accountId}/probe":                     "account.probe",
		"POST /api/agent-accounts/{accountId}/quota-key":                 "account.probe",
		"POST /api/agent-accounts/{accountId}/readings":                  "account.probe",
		"POST /api/agent-accounts/{accountId}/windows":                   "account.manage",
		"POST /api/agent-accounts/{accountId}/windows/{windowId}/repeat": "account.manage",
		"PUT /api/agent-accounts/boost":                                  "account.manage",
		"PUT /api/agent-accounts/capacity/schedule":                      "account.manage",
		"PUT /api/agent-accounts/pins":                                   "run.create",
		"PUT /api/agent-accounts/quota-pool":                             "account.manage",
		"PUT /api/agent-accounts/{accountId}/floor":                      "account.manage", // Handler also requires workspace model_prefs.manage.
		"PUT /api/agent-accounts/{accountId}/label":                      "account.manage",
		"PUT /api/agent-accounts/{accountId}/limit":                      "account.manage",
		"PUT /api/agent-accounts/{accountId}/metadata":                   "account.manage",
		"PUT /api/agent-accounts/{accountId}/model":                      "account.manage",
		"PUT /api/agent-accounts/{accountId}/posture":                    "account.manage",
		"PUT /api/agent-accounts/{accountId}/residency-evidence":         "account.manage|account.probe", // Handler distinguishes owning person from bound host key.
		"PUT /api/agent-accounts/{accountId}/sharing":                    "account.manage",
		"PUT /api/agent-accounts/{accountId}/signals":                    "account.probe",
		"PUT /api/agent-accounts/{accountId}/statusline":                 "account.manage",
		"PUT /api/agent-accounts/{accountId}/usage-probe":                "account.manage", // Handler re-checks the owning person inside the write.
		"PUT /api/settings/quota-warnings":                               "settings.manage",
	})
}
