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

func writeUsageFixture(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func codexDiscoveryFixture(t *testing.T, home, name, cwd string, at time.Time) string {
	t.Helper()
	meta, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": cwd, "timestamp": at}})
	if err != nil {
		t.Fatal(err)
	}
	return writeUsageFixture(t, filepath.Join(home, "sessions", "2026", "10", "01", "rollout-"+name+".jsonl"), string(meta)+"\n")
}

func TestCodexUsageDiscoveryUsesMetadataAndFence(t *testing.T) {
	home, work := t.TempDir(), "/work/exact"
	start := time.Date(2026, 10, 1, 12, 0, 0, 500_000_000, time.UTC)
	codexDiscoveryFixture(t, home, "old", work, start.Add(-time.Second))
	codexDiscoveryFixture(t, home, "equal", work, start)
	codexDiscoveryFixture(t, home, "same-second-before", work, start.Add(-time.Nanosecond))
	codexDiscoveryFixture(t, home, "wrong", work+"/other", start.Add(time.Hour))
	codexDiscoveryFixture(t, home, "z-older", work, start.Add(time.Minute))
	want := codexDiscoveryFixture(t, home, "a-newest", work, start.Add(2*time.Minute))
	writeUsageFixture(t, filepath.Join(home, "sessions", "rollout-malformed.jsonl"), "{}\n")
	writeUsageFixture(t, filepath.Join(home, "sessions", "rollout-large.jsonl"), strings.Repeat("x", heartbeatTitleLineMax+1)+"\n")
	denied := codexDiscoveryFixture(t, home, "denied", work, start.Add(time.Hour))
	if err := os.Link(denied, filepath.Join(home, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(want, filepath.Join(home, "sessions", "rollout-link.jsonl")); err != nil {
		t.Fatal(err)
	}
	writeUsageFixture(t, filepath.Join(home, "sessions", "credentials", "rollout-hidden.jsonl"), "invalid\n")
	o := heartbeatOptions{Harness: "codex", CodexHome: home, Worktree: work, UsageStartedAt: start}
	target, err := resolveHeartbeatUsage(o)
	if err != nil || target.Path != want {
		t.Fatalf("discovery path=%q err=%v", target.Path, err)
	}
	o.UsageStartedAt = time.Time{}
	if target, err := resolveHeartbeatUsage(o); err != nil || target.Path != "" {
		t.Fatal("discovered without registration time")
	}
	o.UsageStartedAt, o.Worktree = start, ""
	if target, err := resolveHeartbeatUsage(o); err != nil || target.Path != "" {
		t.Fatal("discovered without a bound worktree")
	}
}

func grokDiscoveryFixture(t *testing.T, home, id, work string, created time.Time) string {
	t.Helper()
	root := filepath.Join(home, "sessions", encodeURIComponent(work), id)
	meta, err := json.Marshal(map[string]any{"created_at": created})
	if err != nil {
		t.Fatal(err)
	}
	writeUsageFixture(t, filepath.Join(root, "summary.json"), string(meta))
	return writeUsageFixture(t, filepath.Join(root, "usage.json"), `{"updatedAt":"2099-01-01T00:00:00Z","session":{"primaryModelId":"grok-4.7","inputTokens":12,"outputTokens":4,"cachedReadTokens":2}}`)
}

func TestGrokUsageDiscoveryRequiresCreationAndExactWorktree(t *testing.T) {
	home, work := t.TempDir(), "/work/space here"
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	grokDiscoveryFixture(t, home, "oldsession", work, start.Add(-time.Minute))
	grokDiscoveryFixture(t, home, "wrongsession", work+"/other", start.Add(time.Hour))
	want := grokDiscoveryFixture(t, home, "newsession", work, start.Add(time.Minute))
	link := grokDiscoveryFixture(t, home, "linksession", work, start.Add(time.Hour))
	if err := os.Link(filepath.Join(filepath.Dir(link), "summary.json"), filepath.Join(home, "hardlink")); err != nil {
		t.Fatal(err)
	}
	writeUsageFixture(t, filepath.Join(home, "sessions", encodeURIComponent(work), "nometasession", "usage.json"), "{}")
	o := heartbeatOptions{Harness: "grok", GrokHome: home, Worktree: work, UsageStartedAt: start}
	target, err := resolveHeartbeatUsage(o)
	if err != nil || target.Path != want || !target.Snapshot {
		t.Fatalf("discovery path=%q snapshot=%t err=%v", target.Path, target.Snapshot, err)
	}
	if allowedUsagePath("grok", filepath.Join(filepath.Dir(want), "summary.json")) {
		t.Fatal("summary became an allowed usage file")
	}
}

func TestDiscoveredUsageSurvivesRestartWithoutChangingCursor(t *testing.T) {
	for _, source := range []string{"codex", "grok"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			var calls []hbCall
			srv := hbServer(t, &calls, nil)
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(dir)
			o.Harness, o.Worktree, o.CodexHome, o.GrokHome = source, filepath.Join(dir, "work"), filepath.Join(dir, "codex"), filepath.Join(dir, "grok")
			o.HarnessVersion = "fixture"
			session := openUsageSession(t, rt, o)
			start := session.disk.RegisteredAt
			var want string
			if source == "codex" {
				want = codexDiscoveryFixture(t, o.CodexHome, "first", o.Worktree, start.Add(time.Second))
				f, err := os.OpenFile(want, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("{\"type\":\"turn_context\",\"payload\":{\"model\":\"fixture-model\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":12,\"output_tokens\":4,\"cached_input_tokens\":2,\"reasoning_output_tokens\":1}}}}\n")
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("could not append fixture counters")
				}
			} else {
				want = grokDiscoveryFixture(t, o.GrokHome, "firstsession", o.Worktree, start.Add(time.Second))
			}
			target, err := resolveSessionHeartbeatUsage(o, session)
			if err != nil || target.Path != want {
				t.Fatalf("initial resolution %q %v", target.Path, err)
			}
			session.disk.UsageOffset = 23
			if err := saveHeartbeatSession(session); err != nil {
				t.Fatal(err)
			}
			if source == "codex" {
				codexDiscoveryFixture(t, o.CodexHome, "second", o.Worktree, start.Add(time.Minute))
			} else {
				grokDiscoveryFixture(t, o.GrokHome, "secondsession", o.Worktree, start.Add(time.Minute))
			}
			restarted, ok, err := loadHeartbeatSession(&session.hold)
			if err != nil || !ok {
				t.Fatal("could not reload discovery state")
			}
			o.Worktree = "/different/worktree"
			target, err = resolveSessionHeartbeatUsage(o, &restarted)
			if err != nil || target.Path != want || restarted.disk.UsageOffset != 23 {
				t.Fatal("restart changed log or cursor")
			}
			o.Worktree = session.disk.BoundWorktree
			session.disk.UsageOffset = 0
			if err := rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, o, session); err != nil {
				t.Fatal(err)
			}
			posts := usagePosts(calls)
			input := float64(12)
			if source == "grok" {
				input = 14
			}
			if len(posts) != 1 || numField(posts[0], "input_tokens") != input || numField(posts[0], "output_tokens") != 4 || numField(posts[0], "cached_input_tokens") != 2 {
				t.Fatal("discovered counters were not reported")
			}
		})
	}
}
