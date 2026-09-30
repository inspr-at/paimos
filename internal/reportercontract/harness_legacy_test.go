// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harness"
)

// Frozen harness-session/1.0 serialization at 4fbcd22, before AEON-225.
// Status populates an empty controls slice; heartbeat leaves it nil. Both must
// preserve the exact body, including field order, nulls and omitted fields, with
// one additive exception: 1.7 states `finished` in every session payload (AEON-437),
// last, false for an empty session. Nothing else may change.
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
			if want := strings.TrimSuffix(legacy, "}") + `,"finished":false}` + "\n"; out.String() != want {
				t.Fatalf("1.0 bytes changed:\n%s", out.String())
			}
		})
	}
}

// The single 1.5 fixture composes watch evidence (352), vendor-reference presence
// (369) without revealing the vendor reference itself.
func TestCombinedHarnessSessionV15Fixture(t *testing.T) {
	body, err := json.Marshal(harness.Session{HasVendorSessionRef: true,
		Watch: &harness.AttachStatus{RequestID: "39000000-0000-4000-8000-000000000001", OwnerID: "39000000-0000-4000-8000-000000000002", Mode: "lease", ProcessState: "confirmed_exited", State: "detached"}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["has_vendor_session_ref"]) != "true" {
		t.Fatal("vendor reference presence missing")
	}
	const watch = `{"request_id":"39000000-0000-4000-8000-000000000001","owner_id":"39000000-0000-4000-8000-000000000002","mode":"lease","process_state":"confirmed_exited","state":"detached","lease_until":null}`
	if string(fields["watch"]) != watch {
		t.Fatalf("combined watch fixture = %s", fields["watch"])
	}
	for _, name := range []string{"vendor_session_ref", "vendor_ref_digest"} {
		if _, exists := fields[name]; exists {
			t.Fatalf("private %s serialized", name)
		}
	}
}
