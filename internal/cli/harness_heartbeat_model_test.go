// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunHeartbeatFollowsSessionModel(t *testing.T) {
	for _, source := range []string{"claude", "codex"} {
		t.Run(source, func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, _, stderr := heartbeatRuntime(t, srv)
			dir := t.TempDir()
			opts := heartbeatTestOptions(dir)
			opts.Model, opts.Effort = "claude-sonnet", "low"
			path := claudeUsagePath(t, dir, "session.jsonl")
			first := `{"type":"assistant","message":{"model":"claude-sonnet","reasoning_effort":"low"}}` + "\n"
			second := `{"type":"assistant","message":{"model":"claude-opus","reasoning_effort":"high"}}` + "\n"
			if source == "codex" {
				opts.Harness = "codex"
				path = filepath.Join(dir, "sessions", "rollout-test.jsonl")
				first = `{"type":"turn_context","payload":{"model":"claude-sonnet","effort":"low"}}` + "\n"
				second = `{"type":"turn_context","payload":{"model":"claude-opus","effort":"high"}}` + "\n"
				opts.UsageFile = path
			} else {
				opts.Transcript = path
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(first), 0600); err != nil {
				t.Fatal(err)
			}
			n := 0
			err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
				alive: func(int) bool { return true },
				wait: func(context.Context, int, time.Duration) error {
					n++
					switch n {
					case 1:
						// Between ticks, switch models amid malformed and unrelated records.
						body := first + "broken JSON\n" + second + "{partial\n" + `{"type":"user","message":{"model":"wrong-model"}}` + "\n"
						if err := os.WriteFile(path, []byte(body), 0600); err != nil {
							t.Fatal(err)
						}
					case 3:
						// Unreadable evidence must not restore the original flags.
						if err := os.WriteFile(path, []byte("bad JSON\n"), 0600); err != nil {
							t.Fatal(err)
						}
					case 4:
						return errOwnerExited
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("run: %v (%s)", err, stderr.String())
			}
			beats := hbWhere(calls, http.MethodPost, "/heartbeat")
			if len(beats) != 4 {
				t.Fatalf("beats: %d", len(beats))
			}
			if beats[0].body["model"] != nil || beats[0].body["reasoning_effort"] != nil {
				t.Fatal("unchanged registration resent", beats[0].body)
			}
			if beats[1].body["model"] != "claude-opus" || beats[1].body["reasoning_effort"] != "high" {
				t.Fatal("switch missing", beats[1].body)
			}
			for _, beat := range beats[2:] {
				if beat.body["model"] != nil || beat.body["reasoning_effort"] != nil {
					t.Fatal("unchanged or missing evidence resent identity", beat.body)
				}
			}
			if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 {
				t.Fatal("model switch registered a generation")
			}
			for _, beat := range beats {
				if !strings.Contains(beat.path, transcriptSessionID) || beat.lease == "" {
					t.Fatal("switch lost generation proof")
				}
			}
		})
	}
}

func TestHeartbeatModelAcknowledgementAndRestart(t *testing.T) {
	dir := t.TempDir()
	path := claudeUsagePath(t, dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":{"model":"claude-opus","reasoning_effort":"high"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := heartbeatOptions{Harness: "claude", Transcript: path, Model: "claude-sonnet", Effort: "low"}
	s := heartbeatSession{disk: heartbeatDisk{ModelSent: true, SentModel: "claude-sonnet", SentEffort: "low"}}
	for range 2 { // Neither attempt has an accepted response yet.
		body := map[string]any{}
		putHeartbeatModel(context.Background(), opts, &s, body)
		if body["model"] != "claude-opus" || body["reasoning_effort"] != "high" {
			t.Fatal("unacknowledged change lost", body)
		}
	}
	body := map[string]any{}
	putHeartbeatModel(context.Background(), opts, &s, body)
	acceptHeartbeatModel(&s, body)
	hold, err := openHeartbeatHold(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.release()
	s.hold = hold
	s.id = transcriptSessionID
	s.lease = strings.Repeat("a", 40)
	s.disk.Schema = heartbeatSchema
	s.disk.SessionID = s.id
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
	if err := os.WriteFile(path, []byte("invalid JSON\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.Model, opts.Effort = "different-start-flag", "max"
	body = map[string]any{}
	putHeartbeatModel(context.Background(), opts, &resumed, body)
	if len(body) != 0 {
		t.Fatal("accepted identity resent after restart", body)
	}
}

func TestHeartbeatModelBoundedAndFenced(t *testing.T) {
	dir := t.TempDir()
	path := claudeUsagePath(t, dir, "session.jsonl")
	valid := `{"type":"assistant","message":{"model":"claude-opus"}}` + "\n"
	raw := strings.Repeat("x", 17<<20) + "\n" + valid + `{"type":"assistant","message":{"model":"partial"}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	opts := heartbeatOptions{Harness: "claude", Transcript: path}
	if model, effort := readHeartbeatModel(context.Background(), opts, &heartbeatSession{}); model != "claude-opus" || effort != "" {
		t.Fatalf("tail: %q %q", model, effort)
	}
	opts.Transcript = filepath.Join(dir, "auth.json")
	if model, _ := readHeartbeatModel(context.Background(), opts, &heartbeatSession{}); model != "" {
		t.Fatal("credential path accepted")
	}
	link := claudeUsagePath(t, dir, "link.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	opts.Transcript = link
	if model, _ := readHeartbeatModel(context.Background(), opts, &heartbeatSession{}); model != "" {
		t.Fatal("symlink accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts.Transcript = path
	if model, _ := readHeartbeatModel(ctx, opts, &heartbeatSession{}); model != "" {
		t.Fatal("cancelled read returned evidence")
	}
}

func TestHeartbeatModelLineRejectsUnrelatedAndInvalid(t *testing.T) {
	for _, raw := range []string{
		`{"method":"turn/start","params":{"model":"requested-only"}}`,
		`{"method":"model/rerouted","params":{"threadId":"foreign","toModel":"foreign-model"}}`,
		`{"type":"turn_context","payload":{"model":"bad\nmodel"}}`,
		`{"type":"turn_context","payload":{"model":"` + strings.Repeat("x", 129) + `"}}`,
		`{"type":"turn_context","payload":{"model":123}}`,
	} {
		if m, e := heartbeatModelLine("codex", []byte(raw), transcriptSessionID); m != "" || e != "" {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{
		`{"type":"turn_context","payload":{"model":"gpt-test","effort":"xhigh"}}`,
		`{"method":"thread/started","params":{"thread":{"id":"` + transcriptSessionID + `","model":"gpt-test","reasoning_effort":"xhigh"}}}`,
		`{"method":"turn/started","params":{"threadId":"` + transcriptSessionID + `","turn":{"model":"gpt-test","reasoning":{"effort":"xhigh"}}}}`,
	} {
		if m, e := heartbeatModelLine("codex", []byte(raw), transcriptSessionID); m != "gpt-test" || e != "xhigh" {
			t.Fatalf("missed %s: %q %q", raw, m, e)
		}
	}
}

func TestHeartbeatModelLegacyStatePreservesServerIdentity(t *testing.T) {
	opts := heartbeatOptions{Harness: "claude", Model: "stale-start-model", Effort: "low"}
	s := heartbeatSession{}
	body := map[string]any{}
	putHeartbeatModel(context.Background(), opts, &s, body)
	if len(body) != 0 {
		t.Fatal("legacy helper restored stale start flags", body)
	}
}

func TestHeartbeatModelEffortOnlyAndModelOnly(t *testing.T) {
	dir := t.TempDir()
	path := claudeUsagePath(t, dir, "session.jsonl")
	opts := heartbeatOptions{Harness: "claude", Transcript: path, Model: "stale-start-model", Effort: "low"}
	s := heartbeatSession{disk: heartbeatDisk{ModelSent: true, SentModel: "claude-opus", SentEffort: "high"}}
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":{"model":"claude-opus","reasoning_effort":"max"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	putHeartbeatModel(context.Background(), opts, &s, body)
	if len(body) != 1 || body["reasoning_effort"] != "max" {
		t.Fatal("effort-only change incorrect", body)
	}
	acceptHeartbeatModel(&s, body)
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":{"model":"claude-sonnet"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body = map[string]any{}
	putHeartbeatModel(context.Background(), opts, &s, body)
	if len(body) != 1 || body["model"] != "claude-sonnet" {
		t.Fatal("model-only change restored stale effort", body)
	}
}
