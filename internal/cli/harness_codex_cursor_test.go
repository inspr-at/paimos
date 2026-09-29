// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func codexTokens(input, output int64) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":2}}}}`+"\n", input, output)
}

func codexContext(model string) string {
	return `{"type":"turn_context","payload":{"model":"` + model + `"}}` + "\n"
}

func appendFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Review case: turn_context is read on one beat, its token record on the
// next. The model must carry over, with or without a --model fallback.
func TestCodexModelContextSurvivesBeats(t *testing.T) {
	for _, fallback := range []string{"", "gpt-5"} {
		t.Run("fallback="+fallback, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions", "rollout-review.jsonl")
			fenceWrite(t, path, codexContext("gpt-5.4"))
			sums, off, ring, discard, cursor, err := scanUsageWindowState(context.Background(), path, fallback, 0, 1<<20, nil, false, "codex", nil)
			if err != nil || len(sums) != 0 || cursor == nil || cursor.Model != "gpt-5.4" {
				t.Fatalf("first beat: sums=%v cursor=%+v err=%v", sums, cursor, err)
			}
			appendFile(t, path, codexTokens(150, 10))
			sums, _, _, _, _, err = scanUsageWindowState(context.Background(), path, fallback, off, 1<<20, ring, discard, "codex", cursor)
			if err != nil {
				t.Fatal(err)
			}
			if len(sums) != 1 || sums["gpt-5.4"].input != 150 {
				t.Fatalf("model lost between beats: %v", sums)
			}
		})
	}
}

// Review case: cumulative 100 under model A, then 150 under model B is 150
// tokens in total, A:100 and B:50, in one scan or across beats.
func TestCodexModelSwitchAttributesDeltas(t *testing.T) {
	body := codexContext("gpt-5") + codexTokens(100, 10) + codexContext("gpt-5.4") + codexTokens(150, 16)
	path := filepath.Join(t.TempDir(), "sessions", "rollout-review.jsonl")
	fenceWrite(t, path, body)
	sums, _, _, _, err := scanUsageWindowSource(context.Background(), path, "", 0, 1<<20, nil, false, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || sums["gpt-5"].input != 100 || sums["gpt-5"].output != 10 || sums["gpt-5.4"].input != 50 || sums["gpt-5.4"].output != 6 {
		t.Fatalf("model switch attribution: %v", sums)
	}
	// A stale lower total never moves the baseline or adds tokens.
	fenceWrite(t, path, body+codexTokens(120, 12))
	sums, _, _, _, err = scanUsageWindowSource(context.Background(), path, "", 0, 1<<20, nil, false, "codex")
	if err != nil || sums["gpt-5"].input+sums["gpt-5.4"].input != 150 {
		t.Fatalf("stale total counted: %v %v", sums, err)
	}
}

// End to end through the persisted heartbeat state: the context model and
// the session baseline survive between reportHeartbeatUsage calls.
func TestCodexCursorPersistsAcrossReports(t *testing.T) {
	var calls []hbCall
	srv := hbServer(t, &calls, nil)
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "2026", "09", "29", "rollout-2026-09-29T00-00-00-review.jsonl")
	fenceWrite(t, path, codexContext("gpt-5")+codexTokens(100, 10))
	opts := heartbeatTestOptions(dir)
	opts.Harness, opts.UsageSource, opts.UsageFile, opts.Transcript = "codex", "codex", path, ""
	opts.Model = "gpt-4.1"
	session := openUsageSession(t, rt, opts)
	report := func() {
		t.Helper()
		if err := rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
			t.Fatalf("report: %v stderr %s", err, stderr.String())
		}
	}
	report()
	appendFile(t, path, codexContext("gpt-5.4"))
	report()
	appendFile(t, path, codexTokens(150, 16))
	report()
	posts := usagePosts(calls)
	if len(posts) != 2 {
		t.Fatalf("posts %#v", posts)
	}
	if posts[0]["model"] != "gpt-5" || numField(posts[0], "input_tokens") != 100 {
		t.Fatalf("first report %#v", posts[0])
	}
	if posts[1]["model"] != "gpt-5.4" || numField(posts[1], "input_tokens") != 50 || numField(posts[1], "output_tokens") != 6 {
		t.Fatalf("second report lost the model or re-counted the baseline: %#v", posts[1])
	}
	if c := session.disk.UsageCodex; c == nil || c.Model != "gpt-5.4" || c.Input != 150 {
		t.Fatalf("persisted cursor %+v", c)
	}
	// The beat saves the committed cursor; it must round-trip through disk.
	if err := saveHeartbeatSession(session); err != nil {
		t.Fatal(err)
	}
	session.hold.release()
	reopened := openUsageSession(t, rt, opts)
	if c := reopened.disk.UsageCodex; c == nil || c.Model != "gpt-5.4" || c.Input != 150 {
		t.Fatalf("cursor not saved: %+v", c)
	}
}

// State written before the cursor existed held session-wide totals per model.
// Upgrading must not add them again.
func TestLegacyCodexStateDoesNotRecount(t *testing.T) {
	five := int64(5)
	cur := legacyCodexCursor([]heartbeatUsageDisk{{Model: "gpt-5", Input: 100, Output: 10, Reasoning: &five}, {Model: "gpt-5.4", Input: 150, Output: 16}})
	if cur.Input != 150 || cur.Output != 16 || cur.Reasoning != 5 || cur.Model != "" {
		t.Fatalf("legacy cursor %+v", cur)
	}
}
