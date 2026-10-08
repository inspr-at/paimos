// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for phoneapprovals.
func init() {
	registerRoutes("phoneapprovals", map[string]string{
		"DELETE /api/me/phone-approvals/passkeys/{credentialId}":        "profile.write",
		"DELETE /api/me/phone-approvals/subscriptions/{subscriptionId}": "profile.write",
		"GET /api/me/phone-approvals":                                   "profile.read",
		"GET /api/phone-approvals/{kind}/{requestId}":                   "profile.read",
		"POST /api/me/phone-approvals/passkeys":                         "profile.write",
		"POST /api/me/phone-approvals/passkeys/options":                 "profile.write",
		"POST /api/me/phone-approvals/subscriptions":                    "profile.write",
		"POST /api/phone-approvals/{kind}/{requestId}/decision":         "profile.write",
		"POST /api/phone-approvals/{kind}/{requestId}/options":          "profile.write",
		"PUT /api/me/phone-approvals/settings":                          "profile.write",
	})
	registerDeclarations("phoneapprovals", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"DELETE /api/me/phone-approvals/passkeys/{credentialId}":        true,
		"DELETE /api/me/phone-approvals/subscriptions/{subscriptionId}": true,
		"GET /api/me/phone-approvals":                                   true,
		"GET /api/phone-approvals/{kind}/{requestId}":                   true,
		"POST /api/me/phone-approvals/passkeys":                         true,
		"POST /api/me/phone-approvals/passkeys/options":                 true,
		"POST /api/me/phone-approvals/subscriptions":                    true,
		"POST /api/phone-approvals/{kind}/{requestId}/decision":         true,
		"POST /api/phone-approvals/{kind}/{requestId}/options":          true,
		"PUT /api/me/phone-approvals/settings":                          true,
	})
}
