// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/harness"
)

// Frozen harness-session/1.0 serialization at 4fbcd22, before AEON-225.
// Status populates an empty controls slice; heartbeat leaves it nil. Both must
// preserve the exact body, including field order, nulls and omitted fields.
func TestNoRequestStatusAndHeartbeatAreByteIdenticalToV1(t *testing.T) {
	const legacy = `{"archived_at":null,"recovery_process_state":null,"id":"","project_id":"","agent_principal_id":"","run_id":null,"ticket_node_id":null,"work_order_id":null,"parent_harness_session_id":null,"harness":"","host":"","display_label":null,"model":null,"reasoning_effort":null,"account_label":null,"harness_version":null,"brief":null,"worktree":null,"branch":null,"commits":null,"activity_note":null,"management_mode":"","role":"","work_shape":"","advertised_capabilities":null,"phase":"","activity":"","activity_sequence":0,"revision":0,"heartbeat_at":null,"stopped_at":null,"stop_reason":null,"created_at":"0001-01-01T00:00:00Z"}`
	for _, tc := range []struct {
		name     string
		controls []harness.Control
	}{
		{"status", []harness.Control{}}, {"heartbeat", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := json.NewEncoder(&out).Encode(harness.Session{Controls: tc.controls}); err != nil {
				t.Fatal(err)
			}
			if out.String() != legacy+"\n" {
				t.Fatalf("1.0 bytes changed:\n%s", out.String())
			}
		})
	}
}
