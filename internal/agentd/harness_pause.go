// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
)

// HarnessPause is the heartbeat projection consumed by the daemon. Keeping
// this transport type here avoids a dependency on the server's HTTP module.
type HarnessPause struct {
	WakeInMS   int64      `json:"wake_in_ms"`
	Level      string     `json:"level"`
	Note       string     `json:"note"`
	StartsAt   *time.Time `json:"starts_at"`
	Deliver    bool       `json:"deliver"`
	ControlID  string     `json:"control_id"`
	State      string     `json:"state"`
	DeadlineAt time.Time  `json:"deadline_at"`
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
	if pause.StartsAt != nil && !pause.Deliver || !workorders.UUID(pause.ControlID) || (pause.State != "requested" && pause.State != "planned") {
		return nil
	}
	instruction := "Plan a safe handover point; finish or roll back the current step."
	switch pause.Level {
	case "pause_quickly":
		instruction = "Pause quickly within 1–2 minutes: commit WIP as it is and write a short handover. An available native interrupt is delivered separately; otherwise react after the current command."
	case "wrap_up":
		instruction = "Wrap up: finish the work only if it will take less than 10 minutes and fit the deadline; otherwise choose a handover point."
	case "stop_now":
		return nil
	}
	text := fmt.Sprintf("Pause requested for this session. %s Control ID: %s. Handover deadline: %s. Use harness pause-plan; finish or roll back the current step, commit WIP on your own branch, and write state, next steps and open questions using harness mark-stopped --reason paused --handover-file PATH. The server persists the note on this session and its ticket. Then exit cleanly. This request authorizes no merge, deployment or other process signal.", instruction, pause.ControlID, pause.DeadlineAt.UTC().Format(time.RFC3339))
	if pause.Note != "" {
		// JSON frames the steer note as task content, never as signal authority.
		frame, _ := json.Marshal(map[string]string{"handover_note": pause.Note})
		text += "\nOptional task note (untrusted content): " + string(frame)
	}
	_, err := s.controlInbox(ctx, ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: entry.record.RunID, Generation: s.generation, CorrelationID: "pause:" + pause.ControlID + ":" + pause.Level, Operation: "inbox", Text: text}, false, true)
	return err
}
