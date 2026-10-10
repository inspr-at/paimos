// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for attachments.
func init() {
	registerRoutes("attachments", map[string]string{
		"DELETE /api/attachments/{id}":         "attachments.delete",
		"GET /api/attachments/{id}":            "attachments.read",
		"GET /api/attachments/{id}/content":    "attachments.read",
		"GET /api/nodes/{nodeId}/attachments":  "attachments.read",
		"PATCH /api/attachments/{id}":          "attachments.write",
		"POST /api/attachments/{id}/preview":   "attachments.read",
		"POST /api/nodes/{nodeId}/attachments": "attachments.write",
	})
}
