// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for stagehandoff.
func init() {
	registerRoutes("stagehandoff", map[string]string{
		"GET /api/projects/{projectId}/releases/{releaseId}/candidate-artifact": "stage_handoffs.read",
		// Readers, or the routed plugin agent holding that handoff's operation scope,
		// can inspect attempt, supersession and current authority on the existing route.
		"GET /api/stage-handoffs/{handoffId}":                                             "stage_handoffs.read|stage.prepare|stage.deploy|stage.verify|stage.apply",
		"POST /api/projects/{projectId}/baseline-batches/batches/{batchId}/built-receipt": "stage.verify",
		"POST /api/stage-handoffs":                                                        "stage_handoffs.write",
		"POST /api/stage-handoffs/{handoffId}/classic-batch-alias":                        "stage_handoffs.decide",
		"POST /api/stage-handoffs/{handoffId}/evidence":                                   "stage_handoffs.write|stage.prepare|stage.deploy|stage.verify|stage.apply",
		"POST /api/stage-handoffs/{handoffId}/launch/admit":                               "stage.deploy",
		"POST /api/stage-handoffs/{handoffId}/launch/consume":                             "stage.deploy",
		"POST /api/stage-handoffs/{handoffId}/result":                                     "stage_handoffs.decide|stage.prepare|stage.deploy|stage.verify|stage.apply",
		"PUT /api/projects/{projectId}/releases/{releaseId}/candidate-artifact":           "stage_handoffs.write",
	})
}
