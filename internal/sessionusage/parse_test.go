// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestVendorMicrosMatchesAgentd(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
		ok   bool
	}{
		{`0.0000125`, 13, true},
		{`7.00000049`, 7_000_000, true},
		{`1e-6`, 1, true},
		{`-0.01`, 0, false},
		{`null`, 0, false},
		{`100000000000000`, 0, false},
	} {
		got, ok := vendorMicros(json.RawMessage(tc.raw))
		if got != tc.want || ok != tc.ok {
			t.Errorf("vendorMicros(%s) = %d, %t; want %d, %t", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCodexCumulativeFixture(t *testing.T) {
	res := parseFixture(t, "codex", "gpt-6-luna", "codex_token_count.jsonl")
	if len(res.Reports) != 1 || len(res.Observations) != 1 {
		t.Fatalf("reports: %+v", res)
	}
	got := res.Reports[0]
	if got.Model != "gpt-6-luna" || got.Sequence != 5 || got.Provisional || got.BillingMode != "unknown" {
		t.Fatalf("report: %+v", got)
	}
	if *got.InputTokens != 150 || *got.OutputTokens != 35 || *got.CachedInputTokens != 80 {
		t.Fatalf("counters: %+v", got)
	}
	obs := res.Observations[0]
	if obs.Accounting != accountingVendor || obs.Measurement != statusKnown || obs.ModelStatus != statusKnown || obs.CachedStatus != statusKnown {
		t.Fatalf("observation: %+v", obs)
	}
	assertNoLeak(t, res)
}

func TestCodexTurnDeltasSum(t *testing.T) {
	res := parseFixture(t, "codex", "gpt-6-terra", "codex_turn_deltas.jsonl")
	got := res.Reports[0]
	if *got.InputTokens != 17 || *got.OutputTokens != 8 || *got.CachedInputTokens != 6 || got.Provisional {
		t.Fatalf("report: %+v", got)
	}
	if res.Observations[0].Accounting != accountingDelta {
		t.Fatalf("accounting: %+v", res.Observations[0])
	}
}

func TestCursorCanonicalModel(t *testing.T) {
	res := parseFixture(t, "cursor", "", "cursor_result.jsonl")
	got := res.Reports[0]
	if got.Model != "composer-2.5" || *got.InputTokens != 42 || *got.OutputTokens != 8 || *got.CachedInputTokens != 10 || got.Provisional {
		t.Fatalf("report: %+v", got)
	}
	if res.Observations[0].Accounting != accountingDelta || res.Observations[0].Source != "cursor" {
		t.Fatalf("observation: %+v", res.Observations[0])
	}
	assertNoLeak(t, res)
}

func TestJSONRPCCachedUnknownIsProvisional(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","tokenUsage":{"total":{"inputTokens":12,"outputTokens":3},"last":{"inputTokens":12,"outputTokens":3}}}}`
	res := parseSource(t, "codex", "gpt-6-sol", body)
	got := res.Reports[0]
	if *got.InputTokens != 12 || *got.OutputTokens != 3 || got.CachedInputTokens != nil || !got.Provisional {
		t.Fatalf("report: %+v", got)
	}
	if res.Observations[0].CachedStatus != statusUnknown || res.Observations[0].Measurement != statusProvisional || res.Observations[0].Accounting != accountingVendor {
		t.Fatalf("observation: %+v", res.Observations[0])
	}
}

func TestCumulativeKeepsTotalWhenLastIsSmaller(t *testing.T) {
	body := `{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}}}`
	res := parseSource(t, "codex", "gpt-6-luna", body)
	if *res.Reports[0].InputTokens != 100 || *res.Reports[0].CachedInputTokens != 40 {
		t.Fatalf("report: %+v", res.Reports[0])
	}
}

func TestFullPrefixInsteadOfPriorPlusDelta(t *testing.T) {
	opt := testOptions("codex", "gpt-6-luna")
	opt.Final = false
	first := wrapDeltas(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}`) + "\n"
	prior, err := Parse(strings.NewReader(first), opt)
	if err != nil {
		t.Fatal(err)
	}
	second := `{"record_id":"00000000-0000-4000-8000-000000000002","event":{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":2,"output_tokens":5}}}`
	opt.Previous = &prior.Checkpoint
	res, err := Parse(strings.NewReader(first+second), opt)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Reports[0]
	if got.Sequence != 4 || *got.InputTokens != 107 || *got.OutputTokens != 25 || *got.CachedInputTokens != 42 {
		t.Fatalf("report: %+v", got)
	}
	if _, err := Parse(strings.NewReader(second), opt); !errors.Is(err, ErrRejected) {
		t.Fatalf("accepted partial tail: %v", err)
	}
}

func TestEqualCumulativeWithRepeatedLastIsNotAdded(t *testing.T) {
	body := `{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2},"last_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}`
	res := parseSource(t, "codex", "gpt-6-luna", body+"\n"+body)
	if *res.Reports[0].InputTokens != 10 {
		t.Fatalf("report: %+v", res.Reports[0])
	}
}

func TestModelsSortAndStaySeparate(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"turn.completed","model":"gpt-6-terra","usage":{"input_tokens":2,"cached_input_tokens":0,"output_tokens":1}}`,
		`{"type":"turn.completed","model":"gpt-6-luna","usage":{"input_tokens":4,"cached_input_tokens":1,"output_tokens":1}}`,
	}, "\n")
	res := parseSource(t, "codex", "", body)
	if len(res.Reports) != 2 || res.Reports[0].Model != "gpt-6-luna" || res.Reports[1].Model != "gpt-6-terra" {
		t.Fatalf("reports: %+v", res.Reports)
	}
}

func TestCursorWithoutCacheIsProvisional(t *testing.T) {
	body := `{"type":"result","model":"grok-4.7-medium","usage":{"inputTokens":3,"outputTokens":1}}`
	res := parseSource(t, "cursor", "", body)
	if res.Reports[0].CachedInputTokens != nil || !res.Reports[0].Provisional || res.Observations[0].Measurement != statusProvisional {
		t.Fatalf("result: %+v %+v", res.Reports[0], res.Observations[0])
	}
}

func TestMaxTokenAccepted(t *testing.T) {
	body := `{"type":"turn.completed","usage":{"input_tokens":1000000000000,"cached_input_tokens":0,"output_tokens":0}}`
	res := parseSource(t, "codex", "gpt-6-luna", body)
	if *res.Reports[0].InputTokens != maxToken {
		t.Fatalf("input: %d", *res.Reports[0].InputTokens)
	}
}

func TestEmptyInput(t *testing.T) {
	res := parseSource(t, "codex", "gpt-6-luna", "")
	if len(res.Reports) != 0 || len(res.Observations) != 0 {
		t.Fatalf("result: %+v", res)
	}
}

func TestValidCostIsNotReported(t *testing.T) {
	body := `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1},"cost_usd_total":0.0000125}`
	res := parseSource(t, "codex", "gpt-6-luna", body)
	raw, _ := json.Marshal(res)
	if bytesContains(raw, "cost") || bytesContains(raw, "0.0000125") || *res.Reports[0].InputTokens != 1 {
		t.Fatalf("cost leaked: %s", raw)
	}
}

func TestCostOnlyACPIsIgnored(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-secret-value","update":{"sessionUpdate":"usage_update","cost":{"amount":0.0000125,"currency":"USD"}}}}`
	res := parseSource(t, "cursor", "composer-2.5", body)
	if len(res.Reports) != 0 {
		t.Fatalf("reports: %+v", res.Reports)
	}
}

func TestRejects(t *testing.T) {
	for _, tc := range []struct {
		name, source, model, body string
		err                       error
	}{
		{"negative", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":-1,"output_tokens":1}}`, ErrMalformed},
		{"fraction", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1.5,"output_tokens":1}}`, ErrMalformed},
		{"leading zero", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":01,"output_tokens":1}}`, ErrMalformed},
		{"null token", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":null,"output_tokens":1}}`, ErrMalformed},
		{"overflow", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1000000000001,"output_tokens":0}}`, ErrMalformed},
		{"cached exceeds", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":2,"output_tokens":1}}`, ErrRejected},
		{"reasoning exceeds", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"reasoning_output_tokens":2}}`, ErrRejected},
		{"total mismatch", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":3}}`, ErrAmbiguous},
		{"duplicate key", "codex", "gpt-6-luna", `{"type":"turn.completed","type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, ErrAmbiguous},
		{"unknown field", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"prompt":"x"}}`, ErrAmbiguous},
		{"decrease", "codex", "gpt-6-luna", "{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":9,\"output_tokens\":1}}}", ErrRejected},

		{"last exceeds", "codex", "gpt-6-luna", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":1,"output_tokens":1},"last_token_usage":{"input_tokens":2,"output_tokens":1}}}`, ErrRejected},
		{"mixed families", "codex", "gpt-6-luna", "{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}", ErrAmbiguous},
		{"auto model", "cursor", "", `{"type":"result","model":"Auto","usage":{"inputTokens":1,"outputTokens":1,"cacheReadTokens":0,"cacheWriteTokens":0}}`, ErrRejected},
		{"missing model", "codex", "", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, ErrRejected},
		{"model conflict", "codex", "gpt-6-luna", `{"type":"turn.completed","model":"gpt-6-terra","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`, ErrAmbiguous},

		{"mixed spelling", "cursor", "composer-2.5", `{"type":"result","usage":{"inputTokens":2,"input_tokens":2,"outputTokens":1,"output_tokens":1}}`, ErrAmbiguous},

		{"wrong vendor", "cursor", "composer-2.5", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, ErrAmbiguous},
		{"unrecognized usage", "codex", "gpt-6-luna", `{"type":"item.completed","input_tokens":4}`, ErrAmbiguous},
		{"bad cost", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1},"cost_usd_total":-0.01}`, ErrMalformed},
		{"bad currency", "cursor", "composer-2.5", `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"usage_update","cost":{"amount":1,"currency":"EUR"}}}}`, ErrMalformed},
		{"acp tokens", "cursor", "composer-2.5", `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"usage_update","inputTokens":1}}}`, ErrAmbiguous},
		{"not json", "codex", "gpt-6-luna", "not-json", ErrMalformed},
		{"array", "codex", "gpt-6-luna", `[{"type":"turn.completed"}]`, ErrMalformed},
		{"non usd object", "codex", "gpt-6-luna", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1},"cost":{"amount":"0.01","currency":"USD"}}`, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(wrapDeltas(tc.body)), testOptions(tc.source, tc.model))
			if !errors.Is(err, tc.err) {
				t.Fatalf("error %v, want %v", err, tc.err)
			}
		})
	}
}

func TestKnownCachedCannotDisappear(t *testing.T) {
	first := `{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":{"inputTokens":10,"outputTokens":2,"cachedInputTokens":1}}}}`
	second := `{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":{"inputTokens":12,"outputTokens":3}}}}`
	_, err := Parse(strings.NewReader(first+"\n"+second), testOptions("codex", "gpt-6-luna"))
	if !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
}

func TestOversizedLine(t *testing.T) {
	line := `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1},"note":"` + strings.Repeat("a", maxLine) + `"}`
	_, err := Parse(strings.NewReader(line), testOptions("codex", "gpt-6-luna"))
	if !errors.Is(err, ErrMalformed) {
		t.Fatal(err)
	}
}

func TestBillingAndAccount(t *testing.T) {
	body := `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`
	res, err := Parse(strings.NewReader(wrapDeltas(body)), Options{
		Source: "codex", Model: "gpt-6-luna", BillingMode: "subscription", SubscriptionLabel: "Team",
		AccountID: "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE", AccountLabel: "desk",
		SessionID: testSession, SourceSessionID: "thread-1", FromStart: true, Final: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Reports[0]
	if got.BillingMode != "subscription" || got.SubscriptionLabel == nil || *got.SubscriptionLabel != "Team" || *got.AccountID != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" || *got.AccountLabel != "desk" {
		t.Fatalf("report: %+v", got)
	}
	if _, err := Parse(strings.NewReader(body), Options{Source: "codex", Model: "gpt-6-luna", SubscriptionLabel: "Team", SessionID: testSession, SourceSessionID: "thread-1", FromStart: true}); !errors.Is(err, ErrAmbiguous) {
		t.Fatal(err)
	}
}

const testSession = "11111111-1111-4111-8111-111111111111"

func testOptions(source, model string) Options {
	id := "thread-1"
	if source == "cursor" {
		id = "session-secret-value"
	}
	return Options{Source: source, Model: model, SessionID: testSession, SourceSessionID: id, FromStart: true, Final: true}
}

// Synthetic fixture capture assigns one persistent ID per delta without a
// native Cursor request_id. Production hooks must persist these before retries.
func wrapDeltas(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if v["request_id"] != nil {
			continue
		}
		if string(v["type"]) == `"turn.completed"` || string(v["type"]) == `"result"` {
			lines[i] = fmt.Sprintf(`{"record_id":"00000000-0000-4000-8000-%012d","event":%s}`, i+1, line)
		}
	}
	return strings.Join(lines, "\n")
}

func parseFixture(t *testing.T, source, model, name string) Result {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return parseSource(t, source, model, string(raw))
}

func parseSource(t *testing.T, source, model, body string) Result {
	t.Helper()
	res, err := Parse(strings.NewReader(wrapDeltas(body)), testOptions(source, model))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assertNoLeak(t *testing.T, res Result) {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"MUST_NOT_LEAK", "session-secret-value", "request-secret-value", "thread-1"} {
		if bytesContains(raw, needle) {
			t.Fatalf("output contains %s: %s", needle, raw)
		}
	}
}

func bytesContains(raw []byte, needle string) bool {
	return strings.Contains(string(raw), needle)
}
