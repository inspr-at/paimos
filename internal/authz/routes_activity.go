// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for activity.
func init() {
	registerRoutes("activity", map[string]string{
		"DELETE /api/nodes/{nodeId}/comments/{commentId}": "comments.delete",
		"GET /api/nodes/{nodeId}/activity":                "events.read",
		"PATCH /api/nodes/{nodeId}/comments/{commentId}":  "comments.write",
		"POST /api/nodes/{nodeId}/comments":               "comments.write",
	})
}
