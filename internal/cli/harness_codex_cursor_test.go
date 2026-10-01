// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func codexTotalsLine(input, output int64, reasoning string) string {
	r := ""
	if reasoning != "" {
		r = `,"reasoning_output_tokens":` + reasoning
	}
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":%d,"cached_input_tokens":0%s}}}}`+"\n", input, output, r)
}

// Round 3 review case: A reports 100 output without reasoning, then B's
// cumulative 110 output carries 80 reasoning. The reasoning baseline was
// unknown, so B must not receive 80 reasoning against 10 output. Its later
// 80 -> 90 increase must still be attributed within the same scan.
func TestCodexUnknownReasoningBaselineIsNotAttributed(t *testing.T) {
	path := filepath.Join(fenceHome(t), "sessions", "2026", "09", "29", "rollout-probe.jsonl")
	fenceWrite(t, path, codexContext("model-a")+codexTotalsLine(100, 100, "")+codexContext("model-b")+codexTotalsLine(150, 110, "80")+codexTotalsLine(200, 150, "90"))
	sums, _, _, _, cursor, err := scanUsageWindowState(context.Background(), path, "", 0, 1<<20, nil, false, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	for model, sum := range sums {
		if sum.reasoningKnown && sum.reasoning > sum.output {
			t.Errorf("%s: reasoning %d above output %d", model, sum.reasoning, sum.output)
		}
	}
	if a := sums["model-a"]; a.reasoningKnown {
		t.Fatalf("model-a %+v: missing reasoning was inferred", a)
	}
	if b := sums["model-b"]; b.input != 100 || b.output != 50 || !b.reasoningKnown || b.reasoning != 10 {
		t.Fatalf("model-b %+v: expected only the known 80 -> 90 reasoning increase", b)
	}
	if cursor == nil || !cursor.ReasoningKnown || cursor.Reasoning != 90 {
		t.Fatalf("baseline not re-established: %+v", cursor)
	}
	// Once the baseline is known again, the next beat attributes increases.
	fenceWrite(t, path, codexContext("model-b")+codexTotalsLine(10, 10, "")+codexTotalsLine(20, 20, "5"))
	sums, off, ring, discard, cursor, err := scanUsageWindowState(context.Background(), path, "", 0, 1<<20, nil, false, "codex", nil)
	if err != nil || sums["model-b"].reasoningKnown || cursor.Reasoning != 5 || !cursor.ReasoningKnown {
		t.Fatalf("re-baseline: %+v cursor %+v %v", sums, cursor, err)
	}
	appendFile(t, path, codexTotalsLine(30, 30, "9"))
	sums, _, _, _, _, err = scanUsageWindowState(context.Background(), path, "", off, 1<<20, ring, discard, "codex", cursor)
	if err != nil || !sums["model-b"].reasoningKnown || sums["model-b"].reasoning != 4 || sums["model-b"].output != 10 {
		t.Fatalf("known baseline: %+v %v", sums, err)
	}
}

// Missing totals cannot erase already attributed reasoning, and the first
// total after a gap establishes a baseline without adding unknown growth.
func TestCodexReasoningSurvivesUnknownStretchInOneScan(t *testing.T) {
	path := filepath.Join(fenceHome(t), "sessions", "rollout-reasoning-gap.jsonl")
	body := codexContext("model-b") + codexTotalsLine(100, 100, "20") + codexTotalsLine(150, 110, "") + codexTotalsLine(200, 120, "80")
	fenceWrite(t, path, body)
	sums, _, _, _, cursor, err := scanUsageWindowState(context.Background(), path, "", 0, 1<<20, nil, false, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := sums["model-b"]; b.input != 200 || b.output != 120 || !b.reasoningKnown || b.reasoning != 20 {
		t.Fatalf("reasoning before the gap was lost or the baseline was counted: %+v", b)
	}
	if cursor == nil || !cursor.ReasoningKnown || cursor.Reasoning != 80 {
		t.Fatalf("baseline not re-established: %+v", cursor)
	}
	fenceWrite(t, path, body+codexTotalsLine(250, 140, "90"))
	sums, _, _, _, cursor, err = scanUsageWindowState(context.Background(), path, "", 0, 1<<20, nil, false, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := sums["model-b"]; b.input != 250 || b.output != 140 || !b.reasoningKnown || b.reasoning != 30 {
		t.Fatalf("known reasoning around the gap did not accumulate: %+v", b)
	}
	if cursor == nil || cursor.Reasoning != 90 {
		t.Fatalf("final cursor: %+v", cursor)
	}
}

// Round 3 review case end to end: a server that rejects reasoning above
// output never sees such a report. Reading the same records in one beat or
// across beats must retain the same known reasoning increase.
func TestCodexReasoningReportsStayAcceptable(t *testing.T) {
	for _, oneScan := range []bool{true, false} {
		t.Run(fmt.Sprintf("oneScan=%t", oneScan), func(t *testing.T) {
			var calls []hbCall
			srv := hbServer(t, &calls, func(r *http.Request, b map[string]any, w http.ResponseWriter) bool {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") && numField(b, "reasoning_tokens") > numField(b, "output_tokens") {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"reasoning tokens exceed output"}`))
					return true
				}
				return false
			})
			defer srv.Close()
			rt, _, stderr := heartbeatRuntime(t, srv)
			dir := fenceHome(t)
			path := filepath.Join(dir, "sessions", "2026", "09", "29", "rollout-reasoning.jsonl")
			lines := []string{codexContext("model-a") + codexTotalsLine(100, 100, ""), codexContext("model-b") + codexTotalsLine(150, 110, "80"), codexTotalsLine(200, 150, "90")}
			fenceWrite(t, path, "")
			opts := heartbeatTestOptions(dir)
			opts.Harness, opts.UsageSource, opts.UsageFile, opts.Transcript = "codex", "codex", path, ""
			session := openUsageSession(t, rt, opts)
			for i, next := range lines {
				appendFile(t, path, next)
				if oneScan && i < len(lines)-1 {
					continue
				}
				if err := rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
					t.Fatalf("report %d: %v stderr %s", i, err, stderr.String())
				}
			}
			if len(session.disk.PendingUsage) != 0 || strings.Contains(stderr.String(), "rejected") {
				t.Fatalf("queue blocked: pending=%+v stderr=%s", session.disk.PendingUsage, stderr.String())
			}
			var bOutput float64
			var bReasoning any
			for _, post := range usagePosts(calls) {
				if post["model"] == "model-b" {
					bOutput = numField(post, "output_tokens")
					bReasoning = post["reasoning_tokens"]
				}
			}
			if bOutput != 50 || bReasoning != float64(10) {
				t.Fatalf("model-b usage lost known reasoning or counted the unsafe baseline: %v", usagePosts(calls))
			}
			before := len(usagePosts(calls))
			if err := rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
				t.Fatal(err)
			}
			if len(usagePosts(calls)) != before {
				t.Fatalf("unchanged log was reported twice: %v", usagePosts(calls))
			}
		})
	}
}

// A persisted report the server rejects for good is dropped with a log line
// instead of blocking every later report.
func TestRejectedUsageReportDoesNotBlockQueue(t *testing.T) {
	var calls []hbCall
	srv := hbServer(t, &calls, func(r *http.Request, b map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") && b["model"] == "bad-model" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid"}`))
			return true
		}
		return false
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	dir := fenceHome(t)
	path := filepath.Join(dir, "sessions", "2026", "09", "29", "rollout-queue.jsonl")
	fenceWrite(t, path, codexContext("gpt-5")+codexTotalsLine(10, 5, ""))
	opts := heartbeatTestOptions(dir)
	opts.Harness, opts.UsageSource, opts.UsageFile, opts.Transcript = "codex", "codex", path, ""
	session := openUsageSession(t, rt, opts)
	reasoning := int64(99)
	session.disk.PendingUsage = []heartbeatPendingUsage{{Model: "bad-model", Sequence: 1, Input: 1, Output: 1, Reasoning: &reasoning, ReportID: "11111111-1111-5111-8111-111111111111"}}
	if err := rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatalf("rejected report blocked the queue: %v", err)
	}
	if !strings.Contains(stderr.String(), "rejected (400); dropped") {
		t.Fatalf("no log line for the dropped report: %s", stderr.String())
	}
	posts := usagePosts(calls)
	if len(session.disk.PendingUsage) != 0 || len(posts) != 2 || posts[1]["model"] != "gpt-5" || usageByModel(session.disk.Usage, "bad-model") != nil {
		t.Fatalf("later usage not reported: posts=%v pending=%+v", posts, session.disk.PendingUsage)
	}
}

func TestFitUsageReport(t *testing.T) {
	five, nine := int64(5), int64(9)
	prev := &heartbeatUsageDisk{Input: 10, Output: 10, Cached: 2, Reasoning: &five}
	cases := []struct {
		name          string
		prev          *heartbeatUsageDisk
		in, out, c, r int64
		known         bool
		wantR         int64
		wantKnown, ok bool
	}{
		{"valid", nil, 10, 10, 2, 4, true, 4, true, true},
		{"reasoning above output, no prior", nil, 10, 10, 2, 40, true, 0, false, true},
		{"reasoning above output keeps prior", prev, 20, 12, 2, 40, true, 5, true, true},
		{"cached above input", nil, 10, 10, 11, 0, false, 0, false, false},
		{"counter decreased", prev, 9, 10, 2, 5, true, 5, true, false},
		{"uncached input decreased", prev, 11, 10, 4, 5, true, 5, true, false},
		{"known reasoning becomes unknown", prev, 20, 20, 2, 0, false, 0, false, false},
		{"reasoning grows", prev, 20, 20, 2, nine, true, nine, true, true},
	}
	for _, tc := range cases {
		r, known, ok := fitUsageReport(tc.prev, tc.in, tc.out, tc.c, tc.r, tc.known)
		if ok != tc.ok || (ok && (r != tc.wantR || known != tc.wantKnown)) {
			t.Errorf("%s: got r=%d known=%t ok=%t", tc.name, r, known, ok)
		}
	}
}
