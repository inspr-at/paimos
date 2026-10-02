// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
)

// HarnessPause is the heartbeat projection consumed by the daemon. Keeping
// this transport type here avoids a dependency on the server's HTTP module.
type HarnessPause struct {
	ControlID  string    `json:"control_id"`
	State      string    `json:"state"`
	DeadlineAt time.Time `json:"deadline_at"`
}

type pauseHeartbeatAPI interface {
	HeartbeatHarnessPause(context.Context, HarnessSession, string) (*HarnessPause, error)
}

// Only an authenticated heartbeat can offer this cooperative stop. It goes
// through the existing generation-fenced inbox path, including its durable
// at-most-once input receipt. It cannot become interrupt/stop signal authority.
func (s *Supervisor) deliverHarnessPause(ctx context.Context, entry *owned, pause *HarnessPause) error {
	entry.mu.Lock()
	capable := entry.inboxCapable
	entry.mu.Unlock()
	if !capable {
		return nil
	}
	if !workorders.UUID(pause.ControlID) || (pause.State != "requested" && pause.State != "planned") {
		return nil
	}
	text := fmt.Sprintf("Pause requested for this session. Control ID: %s. Handover deadline: %s. Plan a safe handover point using harness pause-plan; finish or roll back the current step, commit WIP on your own branch, and write state, next steps and open questions using harness mark-stopped --reason paused --handover-file PATH. The server persists the note on this session and its ticket. Then exit cleanly. This request authorizes no merge, deployment or other process signal.", pause.ControlID, pause.DeadlineAt.UTC().Format(time.RFC3339))
	_, err := s.controlInbox(ctx, ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: entry.record.RunID, Generation: s.generation, CorrelationID: "pause:" + pause.ControlID, Operation: "inbox", Text: text}, false, true)
	return err
}
