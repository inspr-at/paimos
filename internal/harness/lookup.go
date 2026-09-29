// SPDX-License-Identifier: AGPL-3.0-only

package harness

import "time"

// sessionLookup is the ambient tell check: who owns the generation and whether
// it has ended. It is not a session detail.
type sessionLookup struct {
	ID               string     `json:"id"`
	ProjectID        string     `json:"project_id"`
	AgentPrincipalID string     `json:"agent_principal_id"`
	StoppedAt        *time.Time `json:"stopped_at"`
	ArchivedAt       *time.Time `json:"archived_at"`
}
