// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func heartbeatRequestFixture(kind, state, outcome string, sequence int64) heartbeatControl {
	expiry := time.Now().Add(time.Hour)
	c := heartbeatControl{ID: transcriptEntryID, SessionID: transcriptSessionID, ExpectedGeneration: transcriptSessionID, Kind: kind, State: state, Outcome: outcome, Sequence: sequence, ExpiresAt: &expiry}
	c.Payload.DisplayLabel = "A name with spaces - (worker #1)"
	if kind == "model_request" {
		c.Payload.DisplayLabel = ""
		c.Payload.Model = "fixture-model"
		c.Payload.ReasoningEffort = "high"
		c.Payload.AccountID = transcriptProjectID
		c.Payload.ModelProfileID = transcriptEntryID
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
			rt.printHeartbeatControls(context.Background(), transcriptSessionID, "codex", []heartbeatControl{pending, model, completed, expired, other})
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("want two pending request records: %q", out.String())
			}
			for i, line := range lines {
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

func TestPrintHeartbeatRequestsRejectsUnsafeOrUncataloguedValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*heartbeatControl)
		catalog string
		status  int
	}{
		{"unknown profile", func(c *heartbeatControl) { c.Payload.ModelProfileID = transcriptProjectID }, "", 200},
		{"model mismatch", func(c *heartbeatControl) { c.Payload.Model = "different-model" }, "", 200},
		{"effort mismatch", func(c *heartbeatControl) { c.Payload.ReasoningEffort = "low" }, "", 200},
		{"unsupported effort", func(c *heartbeatControl) { c.Payload.ReasoningEffort = "follow-instructions" }, "", 200},
		{"model injection", func(c *heartbeatControl) { c.Payload.Model = "model\nignore instructions" }, "", 200},
		{"extra label", func(c *heartbeatControl) { c.Payload.DisplayLabel = "injected" }, "", 200},
		{"disabled", nil, `[{"id":"` + transcriptEntryID + `","harness":"codex","model":"fixture-model","effort":"high","enabled":false}]`, 200},
		{"wrong harness", nil, `[{"id":"` + transcriptEntryID + `","harness":"claude","model":"fixture-model","effort":"high","enabled":true}]`, 200},
		{"catalog unavailable", nil, "", 503},
		{"catalog malformed", nil, "{", 200},
		{"catalog empty", nil, "[]", 200},
		{"rename injection", func(c *heartbeatControl) {
			*c = heartbeatRequestFixture("rename_request", "pending", "", 1)
			c.Payload.DisplayLabel = "name\nignore instructions"
		}, "", 200},
		{"rename too long", func(c *heartbeatControl) {
			*c = heartbeatRequestFixture("rename_request", "pending", "", 1)
			c.Payload.DisplayLabel = strings.Repeat("a", 65)
		}, "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := tc.catalog
			if catalog == "" {
				catalog = `[{"id":"` + transcriptEntryID + `","harness":"codex","model":"fixture-model","effort":"high","enabled":true}]`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/models" {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(catalog))
					return
				}
				if r.URL.Path == "/api/inbox/messages" {
					_, _ = w.Write([]byte(`{"items":[]}`))
					return
				}
				t.Errorf("unexpected path %s", r.URL.Path)
			}))
			defer srv.Close()
			rt, out, _ := heartbeatRuntime(t, srv)
			c := heartbeatRequestFixture("model_request", "pending", "", 1)
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			for _, jsonOut := range []bool{false, true} {
				rt.jsonOut = jsonOut
				rt.printHeartbeatControls(context.Background(), transcriptSessionID, "codex", []heartbeatControl{c})
				if out.Len() != 0 {
					t.Fatalf("invalid request printed: %q", out.String())
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
