// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeartbeatAppServerModelRequiresThreadBinding(t *testing.T) {
	for _, tc := range []struct {
		name, id, record string
		want             bool
	}{
		{"both missing", "", `{"method":"turn/started","params":{"model":"gpt-test"}}`, false},
		{"session missing", "", `{"method":"turn/started","params":{"threadId":"own","model":"gpt-test"}}`, false},
		{"thread missing", "own", `{"method":"turn/started","params":{"model":"gpt-test"}}`, false},
		{"reply unbound", "", `{"result":{"model":"gpt-test","thread":{"id":"own"}}}`, false},
		{"foreign thread", "own", `{"method":"turn/started","params":{"threadId":"other","model":"gpt-test"}}`, false},
		{"bound turn", "own", `{"method":"turn/started","params":{"threadId":"own","model":"gpt-test"}}`, true},
		{"bound thread", "own", `{"method":"thread/started","params":{"thread":{"id":"own","model":"gpt-test"}}}`, true},
		{"bound reply", "own", `{"result":{"model":"gpt-test","thread":{"id":"own"}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, effort := heartbeatModelLine("codex", []byte(tc.record), tc.id)
			if tc.want && model != "gpt-test" || !tc.want && (model != "" || effort != "") {
				t.Fatalf("binding: model=%q effort=%q", model, effort)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	raw := `{"method":"thread/started","params":{"thread":{"id":"own","model":"own-model","effort":"low"}}}` + "\n" +
		`{"method":"turn/started","params":{"threadId":"other","model":"foreign-model","effort":"high"}}` + "\n" +
		`{"method":"turn/started","params":{"model":"unbound-model","effort":"max"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if model, effort := readHeartbeatModelFile(context.Background(), path, "codex", harnessCodexStream, "own"); model != "own-model" || effort != "low" {
		t.Fatalf("interleaved capture: %q %q", model, effort)
	}
}

func TestHeartbeatCodexRolloutAuthoritativeOverCapacity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"type":"turn_context","payload":{"model":"older-rollout","effort":"medium"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"current-rollout","effort":"xhigh"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "capture.jsonl")
	if err := os.WriteFile(capture, []byte(`{"result":{"model":"stale-capacity","reasoningEffort":"low","thread":{"id":"`+transcriptSessionID+`"}}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o := heartbeatOptions{Harness: "codex", UsageFile: path, UsageID: transcriptSessionID,
		Capacity: heartbeatCapacity{Source: "codex", File: capture}}
	if model, effort := readHeartbeatModel(context.Background(), o, &heartbeatSession{}); model != "current-rollout" || effort != "xhigh" {
		t.Fatalf("stale capacity masked rollout: %q %q", model, effort)
	}
	if err := os.WriteFile(path, []byte("invalid JSON\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if model, effort := readHeartbeatModel(context.Background(), o, &heartbeatSession{}); model != "" || effort != "" {
		t.Fatalf("capacity became a fallback: %q %q", model, effort)
	}
}

func TestHeartbeatAppliedModelRequestFencesHistoricalTranscript(t *testing.T) {
	for _, source := range []string{"claude", "codex"} {
		for _, oldEffort := range []string{"", "low"} {
			t.Run(source+"/"+oldEffort, func(t *testing.T) {
				dir := t.TempDir()
				path := claudeUsagePath(t, dir, "session.jsonl")
				o := heartbeatOptions{Harness: source, Model: "original-model", Effort: "low", Transcript: path}
				line := func(model, effort string) string {
					if source == "codex" {
						return `{"type":"turn_context","payload":{"model":"` + model + `","effort":"` + effort + `"}}` + "\n"
					}
					return `{"type":"assistant","message":{"model":"` + model + `","reasoning_effort":"` + effort + `"}}` + "\n"
				}
				if source == "codex" {
					path = filepath.Join(dir, "sessions", "rollout-test.jsonl")
					o.Transcript, o.UsageFile, o.UsageID = "", path, transcriptSessionID
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
				}
				history := line("original-model", oldEffort)
				if err := os.WriteFile(path, []byte(history), 0600); err != nil {
					t.Fatal(err)
				}
				s := heartbeatSession{id: transcriptSessionID, disk: heartbeatDisk{ModelSent: true, SentModel: "original-model", SentEffort: "low"}}
				request := heartbeatRequestFixture("model_request", "completed", "applied", 1)
				applyHeartbeatRequests(o, heartbeatDeps{}, &s, []heartbeatControl{request}, context.Background())
				for attempt := range 3 {
					body := map[string]any{}
					putHeartbeatModel(context.Background(), o, &s, body)
					if attempt < 2 && (body["model"] != "fixture-model" || body["reasoning_effort"] != "high") {
						t.Fatalf("history discarded applied request or mixed identities: %#v", body)
					}
					if attempt == 1 {
						acceptHeartbeatModel(&s, body)
					}
					if attempt == 2 && len(body) != 0 {
						t.Fatalf("unchanged request resent after restart: %#v", body)
					}
					// The observation baseline must survive helper state persistence.
					raw, err := json.Marshal(s.disk)
					if err != nil {
						t.Fatal(err)
					}
					var restored heartbeatDisk
					if err := json.Unmarshal(raw, &restored); err != nil {
						t.Fatal(err)
					}
					s.disk = restored
				}
				if err := os.WriteFile(path, []byte(history+line("observed-model", "max")), 0600); err != nil {
					t.Fatal(err)
				}
				body := map[string]any{}
				putHeartbeatModel(context.Background(), o, &s, body)
				if body["model"] != "observed-model" || body["reasoning_effort"] != "max" || s.disk.RequestedModel != "" {
					t.Fatalf("subsequent evidence failed to supersede request: %#v", body)
				}
			})
		}
	}
}

func TestHeartbeatAppliedEffortSurvivesModelOnlyObservationAndRestart(t *testing.T) {
	for _, source := range []string{"claude", "codex"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			path := claudeUsagePath(t, dir, "session.jsonl")
			o := heartbeatOptions{Harness: source, Model: "original-model", Effort: "low", Transcript: path}
			line := func(effort string) string {
				if source == "codex" {
					return `{"type":"turn_context","payload":{"model":"original-model","effort":"` + effort + `"}}` + "\n"
				}
				return `{"type":"assistant","message":{"model":"original-model","reasoning_effort":"` + effort + `"}}` + "\n"
			}
			if source == "codex" {
				path = filepath.Join(dir, "sessions", "rollout-test.jsonl")
				o.Transcript, o.UsageFile, o.UsageID = "", path, transcriptSessionID
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
			}
			history := line("low")
			if err := os.WriteFile(path, []byte(history), 0600); err != nil {
				t.Fatal(err)
			}
			s := heartbeatSession{id: transcriptSessionID, disk: heartbeatDisk{ModelSent: true, SentModel: "original-model", SentEffort: "low"}}
			request := heartbeatRequestFixture("model_request", "completed", "applied", 1)
			request.Payload.Model = "original-model"
			applyHeartbeatRequests(o, heartbeatDeps{}, &s, []heartbeatControl{request}, context.Background())
			body := map[string]any{}
			putHeartbeatModel(context.Background(), o, &s, body)
			if len(body) != 1 || body["reasoning_effort"] != "high" {
				t.Fatalf("applied effort-only change missing: %#v", body)
			}
			acceptHeartbeatModel(&s, body)
			history += line("")
			if err := os.WriteFile(path, []byte(history), 0600); err != nil {
				t.Fatal(err)
			}
			body = map[string]any{}
			putHeartbeatModel(context.Background(), o, &s, body)
			if len(body) != 0 {
				t.Fatalf("model-only observation changed accepted effort: %#v", body)
			}
			acceptHeartbeatModel(&s, body)

			hold, err := openHeartbeatHold(filepath.Join(dir, "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer hold.release()
			s.hold, s.lease = hold, strings.Repeat("a", 40)
			if err := hold.writeFile("lease.key", []byte(s.lease+"\n")); err != nil {
				t.Fatal(err)
			}
			if err := saveHeartbeatSession(&s); err != nil {
				t.Fatal(err)
			}
			resumed, ok, err := loadHeartbeatSession(&hold)
			if err != nil || !ok {
				t.Fatalf("resume: %v %v", ok, err)
			}
			for beat := range 2 {
				body = map[string]any{}
				putHeartbeatModel(context.Background(), o, &resumed, body)
				if len(body) != 0 {
					t.Fatalf("heartbeat %d after restart replayed historical effort: %#v", beat+1, body)
				}
				acceptHeartbeatModel(&resumed, body)
				if resumed.disk.SentModel != "original-model" || resumed.disk.SentEffort != "high" {
					t.Fatal("accepted identity lost after restart")
				}
			}
			if err := os.WriteFile(path, []byte(history+line("max")), 0600); err != nil {
				t.Fatal(err)
			}
			body = map[string]any{}
			putHeartbeatModel(context.Background(), o, &resumed, body)
			if len(body) != 1 || body["reasoning_effort"] != "max" {
				t.Fatalf("new effort evidence failed to supersede applied effort: %#v", body)
			}
		})
	}
}

func TestHeartbeatCodexRerouteDoesNotPersistAcrossTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	ordinary := `{"method":"turn/started","params":{"threadId":"own","turn":{"id":"ordinary"}}}` + "\n"
	raw := `{"method":"thread/started","params":{"thread":{"id":"own","model":"session-model","effort":"high"}}}` + "\n" +
		`{"method":"model/rerouted","params":{"threadId":"own","turnId":"rerouted","toModel":"temporary-model"}}` + "\n"
	for _, records := range []string{raw, raw + ordinary} {
		if err := os.WriteFile(path, []byte(records), 0600); err != nil {
			t.Fatal(err)
		}
		if model, effort := readHeartbeatModelFile(context.Background(), path, "codex", harnessCodexStream, "own"); model != "session-model" || effort != "high" {
			t.Fatalf("reroute became session identity: %q %q", model, effort)
		}
	}
}
