// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const deltaOne = `{"record_id":"00000000-0000-4000-8000-000000000001","event":{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":3}}}`
const deltaTwo = `{"record_id":"00000000-0000-4000-8000-000000000002","event":{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":2,"output_tokens":5}}}`

func mustParse(t *testing.T, body string, opt Options) Result {
	t.Helper()
	r, err := Parse(strings.NewReader(body), opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func wantError(t *testing.T, body string, opt Options, want error) {
	t.Helper()
	r, err := Parse(strings.NewReader(body), opt)
	if !errors.Is(err, want) || len(r.Reports) != 0 {
		t.Fatalf("result=%+v err=%v want=%v", r, err, want)
	}
}

func TestRetryStableReceiptsAndFinalization(t *testing.T) {
	opt := testOptions("codex", "model-a")
	opt.Final = false
	first := mustParse(t, deltaOne, opt)
	retry := mustParse(t, deltaOne, opt)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("fresh process retry changes receipt")
	}
	if !first.Reports[0].Provisional || first.Observations[0].Measurement != statusKnown {
		t.Fatal("known counts finalized a running session")
	}
	opt.FromStart = false
	opt.Previous = &first.Checkpoint
	if got := mustParse(t, deltaOne, opt); !reflect.DeepEqual(first, got) {
		t.Fatal("checkpoint retry changes receipt")
	}
	second := mustParse(t, deltaOne+"\n"+deltaTwo, opt)
	if second.Reports[0].Sequence <= first.Reports[0].Sequence || *second.Reports[0].InputTokens != 17 {
		t.Fatal("continuation did not advance exactly once")
	}
	opt.Previous = &second.Checkpoint
	if got := mustParse(t, deltaOne+"\n"+deltaTwo, opt); !reflect.DeepEqual(second, got) {
		t.Fatal("retry after checkpoint re-added deltas")
	}
	opt.Final = true
	final := mustParse(t, deltaOne+"\n"+deltaTwo, opt)
	if final.Reports[0].Provisional || final.Reports[0].Sequence <= second.Reports[0].Sequence || final.Reports[0].ReportID == second.Reports[0].ReportID {
		t.Fatal("final settlement not a new receipt")
	}
	opt.Previous = &final.Checkpoint
	if got := mustParse(t, deltaOne+"\n"+deltaTwo, opt); !reflect.DeepEqual(final, got) {
		t.Fatal("final retry unstable")
	}
	wantError(t, deltaOne+"\n"+deltaTwo+"\n", opt, ErrRejected)
	opt.Final = false
	wantError(t, deltaOne+"\n"+deltaTwo, opt, ErrRejected)
}

func TestCaptureIdentityAndContinuityRequired(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Options)
	}{
		{"missing assertion", func(o *Options) { o.FromStart = false }},
		{"missing Aeon session", func(o *Options) { o.SessionID = "" }},
		{"missing source session", func(o *Options) { o.SourceSessionID = "" }},
		{"invalid source session", func(o *Options) { o.SourceSessionID = "name with text" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testOptions("codex", "model-a")
			tc.change(&o)
			wantError(t, deltaOne, o, ErrRejected)
		})
	}
	o := testOptions("codex", "model-a")
	o.Final = false
	first := mustParse(t, deltaOne+"\n", o)
	o.FromStart = false
	o.Previous = &first.Checkpoint
	for _, body := range []string{deltaTwo, strings.Replace(deltaOne, "10", "11", 1) + "\n", deltaOne[:len(deltaOne)-1], ""} {
		wantError(t, body, o, ErrRejected)
	}
	for _, change := range []func(*Options){
		func(o *Options) { o.SessionID = "22222222-2222-4222-8222-222222222222" },
		func(o *Options) { o.SourceSessionID = "thread-2" }, func(o *Options) { o.Source = "cursor" },
		func(o *Options) { o.Model = "model-b" }, func(o *Options) { o.BillingMode = "api" },
	} {
		changed := o
		change(&changed)
		wantError(t, deltaOne+"\n"+deltaTwo, changed, ErrRejected)
	}
}

func TestDeltaIdentityDeduplicatesAndRejectsConflictingReuse(t *testing.T) {
	o := testOptions("codex", "model-a")
	one := mustParse(t, deltaOne, o)
	twice := mustParse(t, deltaOne+"\n"+deltaOne, o)
	if !reflect.DeepEqual(one.Reports, twice.Reports) {
		t.Fatal("duplicate delta changed receipt")
	}
	wantError(t, deltaOne+"\n"+strings.Replace(deltaOne, `"input_tokens":10`, `"input_tokens":11`, 1), o, ErrAmbiguous)
	wantError(t, `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":3}}`, o, ErrAmbiguous)
	o = testOptions("cursor", "model-a")
	body := `{"type":"result","session_id":"session-secret-value","request_id":"turn-1","usage":{"inputTokens":2,"outputTokens":1,"cacheReadTokens":4,"cacheWriteTokens":8}}`
	one = mustParse(t, body, o)
	twice = mustParse(t, body+"\n"+body, o)
	if !reflect.DeepEqual(one.Reports, twice.Reports) || *twice.Reports[0].InputTokens != 14 || *twice.Reports[0].CachedInputTokens != 4 {
		t.Fatal("Cursor duplicate or category normalization wrong")
	}
	wantError(t, body+"\n"+strings.Replace(body, `"outputTokens":1`, `"outputTokens":2`, 1), o, ErrAmbiguous)
}

func TestModelAttributionNeverInheritsPreviousRecord(t *testing.T) {
	o := testOptions("codex", "")
	identified := strings.Replace(deltaOne, `"type":"turn.completed"`, `"type":"turn.completed","model":"model-a"`, 1)
	wantError(t, identified+"\n"+deltaTwo, o, ErrRejected)
	cumulative := `{"type":"token_count","model":"model-a","info":{"total_token_usage":{"input_tokens":10,"output_tokens":1}}}`
	wantError(t, cumulative, o, ErrAmbiguous)
	o.Model = "model-a"
	wantError(t, cumulative+"\n"+strings.Replace(cumulative, "model-a", "model-b", 1), o, ErrAmbiguous)
	wantError(t, `{"type":"turn_context","payload":{"model":"model-b"}}`+"\n"+cumulative, o, ErrAmbiguous)
}

func TestSessionBoundariesAreRejected(t *testing.T) {
	for _, body := range []string{
		`{"type":"thread.started","thread_id":"other"}`,
		`{"type":"session_meta","payload":{"id":"other","instructions":"MUST_NOT_LEAK"}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"other","tokenUsage":{"total":{"inputTokens":1,"outputTokens":1}}}}`,
	} {
		wantError(t, body, testOptions("codex", "model-a"), ErrRejected)
	}
	wantError(t, `{"type":"system","subtype":"init","session_id":"other"}`, testOptions("cursor", "model-a"), ErrRejected)
	wantError(t, `{"type":"result","session_id":"other","request_id":"req-1","usage":{"inputTokens":1,"outputTokens":1}}`, testOptions("cursor", "model-a"), ErrRejected)
}

func TestCumulativeLatestCallIsNotCaptureStep(t *testing.T) {
	one := `{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":1,"cached_input_tokens":2},"last_token_usage":{"input_tokens":10,"output_tokens":1}}}`
	two := `{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":4,"cached_input_tokens":5},"last_token_usage":{"input_tokens":3,"output_tokens":1,"cached_input_tokens":1}}}`
	r := mustParse(t, one+"\n"+two+"\n"+two, testOptions("codex", "model-a"))
	if *r.Reports[0].InputTokens != 20 || *r.Reports[0].OutputTokens != 4 {
		t.Fatal("coalesced or duplicate cumulative snapshots were miscounted")
	}
	uncachedDecrease := `{"type":"token_count","info":{"total_token_usage":{"input_tokens":11,"output_tokens":2,"cached_input_tokens":10}}}`
	wantError(t, one+"\n"+uncachedDecrease, testOptions("codex", "model-a"), ErrRejected)
}

func TestUnknownDeltaCacheStaysUnknown(t *testing.T) {
	unknown := strings.Replace(deltaOne, `"cached_input_tokens":4,`, "", 1)
	for _, body := range []string{unknown + "\n" + deltaTwo, deltaTwo + "\n" + unknown} {
		r := mustParse(t, body, testOptions("codex", "model-a"))
		if r.Reports[0].CachedInputTokens != nil || *r.Reports[0].InputTokens != 17 || !r.Reports[0].Provisional {
			t.Fatal("partial cached total reported as measured")
		}
	}
	o := testOptions("codex", "model-a")
	o.Final = false
	before := mustParse(t, deltaTwo+"\n", o)
	o.Previous = &before.Checkpoint
	wantError(t, deltaTwo+"\n"+unknown, o, ErrRejected)
}

func TestCursorMissingCacheCannotBecomeInclusiveMeasuredZero(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		cached      *int64
	}{
		{"neither", `{"inputTokens":0,"outputTokens":1}`, nil},
		{"read only", `{"inputTokens":2,"outputTokens":1,"cacheReadTokens":5}`, newInt(5)},
		{"write only", `{"inputTokens":2,"outputTokens":1,"cacheWriteTokens":5}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"type":"result","request_id":"r1","usage":` + tc.usage + `}`
			r := mustParse(t, body, testOptions("cursor", "model-a"))
			if r.Reports[0].InputTokens != nil || !reflect.DeepEqual(r.Reports[0].CachedInputTokens, tc.cached) || *r.Reports[0].OutputTokens != 1 || !r.Reports[0].Provisional {
				t.Fatalf("report: %+v", r.Reports[0])
			}
		})
	}
}
func newInt(n int64) *int64 { return &n }

func TestCounterAndCaptureBounds(t *testing.T) {
	o := testOptions("codex", "model-a")
	max := strings.Replace(deltaOne, `"input_tokens":10`, `"input_tokens":1000000000000`, 1)
	wantError(t, max+"\n"+deltaTwo, o, ErrRejected)
	var records []string
	for i := 0; i <= maxUsage; i++ {
		records = append(records, fmt.Sprintf(`{"record_id":"00000000-0000-4000-8000-%012d","event":{"type":"turn.completed","usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0}}}`, i+1))
	}
	wantError(t, strings.Join(records, "\n"), o, ErrRejected)
	wantError(t, strings.Repeat(" \n", maxInput/2+1), o, ErrMalformed)
	wantError(t, deltaOne+"\n"+`{"record_id":`, o, ErrMalformed)
	for _, usage := range []string{
		`{"inputTokens":1000000000000,"outputTokens":0,"cacheReadTokens":1,"cacheWriteTokens":0}`,
		`{"inputTokens":0,"outputTokens":0,"cacheReadTokens":1000000000000,"cacheWriteTokens":1}`,
	} {
		wantError(t, `{"type":"result","request_id":"r1","usage":`+usage+`}`, testOptions("cursor", "model-a"), ErrRejected)
	}
}

func TestSourceTextNeverEscapesEvenInDiagnostics(t *testing.T) {
	for _, body := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"MUST_NOT_LEAK":"x"}}`,
		`{"MUST_NOT_LEAK":1,"MUST_NOT_LEAK":2}`,
		`{"type":"turn.completed","model":"MUST_NOT_LEAK arbitrary words","usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		_, err := Parse(strings.NewReader(body), testOptions("codex", "model-a"))
		if err == nil || strings.Contains(err.Error(), "MUST_NOT_LEAK") {
			t.Fatalf("diagnostic privacy: %v", err)
		}
	}
	body := `{"type":"item.completed","item":{"type":"agent_message","text":"MUST_NOT_LEAK","usage":{"input_tokens":"MUST_NOT_LEAK"}}}` + "\n" + deltaOne
	r := mustParse(t, body, testOptions("codex", "model-a"))
	assertNoLeak(t, r)
}

func TestEndpointIdentifierAndLabelLimits(t *testing.T) {
	o := testOptions("codex", strings.Repeat("m", 128))
	o.AccountLabel = strings.Repeat("界", 128)
	r := mustParse(t, deltaOne, o)
	if len(r.Reports[0].Model) != 128 || *r.Reports[0].AccountLabel != o.AccountLabel {
		t.Fatal("128 limits diverge from US1")
	}
	for _, label := range []string{strings.Repeat("界", 129), "space ", "x\u0085y"} {
		bad := o
		bad.AccountLabel = label
		wantError(t, deltaOne, bad, ErrRejected)
	}
}

func TestCostExponentCannotAllocateUnboundedNumbers(t *testing.T) {
	for _, cost := range []string{"1e999999999999999999", "1e-999999999999999999", "1e65", "NaN", "1/2"} {
		if _, ok := vendorMicros(json.RawMessage(cost)); ok {
			t.Fatal("accepted hostile or unsupported cost")
		}
	}
}

func TestMissingTerminalUsageCannotSilentlyUndercount(t *testing.T) {
	for _, body := range []string{
		`{"type":"turn.completed"}`,
		`{"type":"turn.failed","error":{"message":"MUST_NOT_LEAK"}}`,
		`{"type":"error","message":"MUST_NOT_LEAK"}`,
	} {
		wantError(t, deltaOne+"\n"+body, testOptions("codex", "model-a"), ErrRejected)
	}
	wantError(t, `{"type":"result","request_id":"r1"}`, testOptions("cursor", "model-a"), ErrRejected)
}

func TestWrongVendorNestedUsageAndCompetingFormats(t *testing.T) {
	wantError(t, `{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":{"inputTokens":1,"outputTokens":1}}}}`, testOptions("cursor", "model-a"), ErrAmbiguous)
	wantError(t, `{"method":"session/update","params":{"update":{"sessionUpdate":"usage_update","inputTokens":1}}}`, testOptions("codex", "model-a"), ErrAmbiguous)
	wantError(t, `{"type":"turn.completed","method":"thread/tokenUsage/updated","usage":{"input_tokens":1,"output_tokens":1}}`, testOptions("codex", "model-a"), ErrAmbiguous)
}

func TestPartialCursorCounterOverflowAndConflictingDuplicate(t *testing.T) {
	o := testOptions("cursor", "model-a")
	one := `{"type":"result","request_id":"r1","usage":{"inputTokens":1000000000000,"outputTokens":1}}`
	wantError(t, one+"\n"+strings.Replace(one, "r1", "r2", 1), o, ErrRejected)
	wantError(t, one+"\n"+strings.Replace(one, "1000000000000", "3", 1), o, ErrAmbiguous)
}

func TestCheckpointRejectsNullCountAndDuplicateKeys(t *testing.T) {
	c := mustParse(t, deltaOne, testOptions("codex", "model-a")).Checkpoint
	b, _ := json.Marshal(c)
	for _, raw := range []string{
		strings.Replace(string(b), fmt.Sprintf(`"bytes":%d`, c.Bytes), `"bytes":null`, 1),
		strings.Replace(string(b), `"final":true`, `"final":true,"final":false`, 1),
		strings.Replace(string(b), `"final":true`, `"final":null`, 1),
	} {
		if _, err := ParseCheckpoint([]byte(raw)); err == nil {
			t.Fatal("accepted malformed checkpoint")
		}
	}
}
