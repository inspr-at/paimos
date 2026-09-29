// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func heartbeatRequestFixture(kind, state, outcome string, sequence int64) heartbeatControl {
	expiry := time.Now().Add(time.Hour)
	c := heartbeatControl{ID: transcriptEntryID, SessionID: transcriptSessionID, ExpectedGeneration: transcriptSessionID, Kind: kind, State: state, Outcome: outcome, Sequence: sequence, ExpiresAt: &expiry}
	c.Payload.DisplayLabel = "A name with spaces — and \"quotes\""
	if kind == "model_request" {
		c.Payload.DisplayLabel = ""
		c.Payload.Model = "fixture-model"
		c.Payload.ReasoningEffort = "high"
	}
	return c
}

func TestPrintHeartbeatSessionRequests(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOut], func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, out, _ := heartbeatRuntime(t, srv)
			rt.jsonOut = jsonOut
			pending := heartbeatRequestFixture("rename_request", "pending", "", 1)
			model := heartbeatRequestFixture("model_request", "claimed", "", 2)
			completed := heartbeatRequestFixture("rename_request", "completed", "applied", 3)
			expired := pending
			past := time.Now().Add(-time.Minute)
			expired.ExpiresAt = &past
			other := pending
			other.ExpectedGeneration = transcriptProjectID
			rt.printHeartbeatControls(context.Background(), transcriptSessionID, []heartbeatControl{pending, model, completed, expired, other})
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("want two pending request records: %q", out.String())
			}
			for i, line := range lines {
				if !jsonOut {
					if !strings.HasPrefix(line, "request ") {
						t.Fatal(line)
					}
					line = strings.TrimPrefix(line, "request ")
				}
				var got struct {
					Type   string `json:"type"`
					Schema string `json:"schema"`
					heartbeatControl
				}
				if err := json.Unmarshal([]byte(line), &got); err != nil {
					t.Fatal(err)
				}
				if got.Type != "request" || got.Schema != "aeon.session-request.v1" || got.ExpectedGeneration != transcriptSessionID || got.Sequence != int64(i+1) {
					t.Fatalf("bad record: %#v", got)
				}
				if i == 0 && got.Payload.DisplayLabel != pending.Payload.DisplayLabel {
					t.Fatal("label lost escaping")
				}
			}
		})
	}
}

func TestHeartbeatReportsOnlyAppliedRequests(t *testing.T) {
	for _, outcome := range []string{"pending", "rejected", "applied"} {
		t.Run(outcome, func(t *testing.T) {
			rename := heartbeatRequestFixture("rename_request", "completed", outcome, 1)
			model := heartbeatRequestFixture("model_request", "completed", outcome, 2)
			if outcome == "pending" {
				rename.State = "pending"
				rename.Outcome = ""
				model.State = "pending"
				model.Outcome = ""
			}
			raw, _ := json.Marshal(map[string]any{"controls": []heartbeatControl{rename, model}})
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, string(raw), "")
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			o.PrintControls = true
			o.Model = "original-model"
			o.Effort = "low"
			n := 0
			err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{alive: func(int) bool { return true }, label: func() (string, bool) { return "Original name", true }, wait: func(context.Context, int, time.Duration) error {
				n++
				if n == 2 {
					return errOwnerExited
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			beats := hbWhere(calls, "POST", "/heartbeat")
			if len(beats) != 2 {
				t.Fatalf("beats: %d", len(beats))
			}
			for i, beat := range beats {
				if outcome == "applied" {
					if beat.body["model"] != "fixture-model" || beat.body["reasoning_effort"] != "high" {
						t.Fatal("applied model was overwritten by flags")
					}
					if i == 0 && beat.body["display_label"] != rename.Payload.DisplayLabel {
						t.Fatal("applied rename not sent")
					}
					if i == 1 && beat.body["display_label"] != nil {
						t.Fatal("stale local name reverted applied rename")
					}
				} else if beat.body["model"] != "original-model" || beat.body["reasoning_effort"] != "low" || beat.body["display_label"] != nil {
					t.Fatal("unapplied request changed metadata")
				}
			}
		})
	}
}

func TestHeartbeatRequestCompletionOrderAndPersistence(t *testing.T) {
	o := heartbeatTestOptions(t.TempDir())
	s := heartbeatSession{id: transcriptSessionID}
	dep := heartbeatDeps{label: func() (string, bool) { return "Original", true }}
	model := heartbeatRequestFixture("model_request", "completed", "applied", 2)
	rename := heartbeatRequestFixture("rename_request", "completed", "applied", 1)
	applyHeartbeatRequests(o, dep, &s, []heartbeatControl{model}, context.Background())
	applyHeartbeatRequests(o, dep, &s, []heartbeatControl{rename}, context.Background())
	if s.disk.RequestedLabel != rename.Payload.DisplayLabel || s.disk.RequestedModel != "fixture-model" {
		t.Fatal("completion order lost independent settings")
	}
	raw, _ := json.Marshal(s.disk)
	var restored heartbeatDisk
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.AppliedRenameSequence != 1 || restored.AppliedModelSequence != 2 {
		t.Fatal("request replay fences not durable")
	}
}

func TestCompleteControlCLIOutcomes(t *testing.T) {
	for _, outcome := range []string{"applied", "rejected"} {
		t.Run(outcome, func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			heartbeatRuntime(t, srv)
			path := filepath.Join(t.TempDir(), "worker-proof")
			if err := os.WriteFile(path, []byte("fixture-session-request-lease-0000000001"), 0600); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := runCLI([]string{"aeon", "harness", "complete-control", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", path, "--control-id", transcriptEntryID, "--outcome", outcome, "--reason", "harness_reported", "--json"}, "")
			if code != 0 {
				t.Fatalf("complete: %d %s", code, stderr)
			}
			posts := hbWhere(calls, "POST", "/complete")
			if len(posts) != 1 || posts[0].body["outcome"] != outcome || posts[0].body["reason"] != "harness_reported" || posts[0].lease == "" {
				t.Fatal("completion lost proof or outcome")
			}
		})
	}
}
