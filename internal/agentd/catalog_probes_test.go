// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/modelreport"
)

func harnessCatalogFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/catalog/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Risk: missing native fields, identifier suffixes and configuration variants
// become invented levels or hide native metadata needed for retirement.
func TestHarnessCatalogRecordedFactsAndUnknown(t *testing.T) {
	codex, next, err := parseCodexCatalog(harnessCatalogFixture(t, "codex.json"))
	if err != nil || next != "" || len(codex) != 2 {
		t.Fatal(codex, next, err)
	}
	for key, want := range map[string]string{"native_levels": `["max","low","focused"]`, "default_level": `"max"`, "hidden": "true", "successor": `"replacement-model"`, "retirement_at": `"2026-10-11T00:00:00Z"`} {
		if string(codex[0].Facts[key].Value) != want {
			t.Fatalf("%s: %s", key, codex[0].Facts[key].Value)
		}
	}
	if len(codex[1].Facts) != 0 {
		t.Fatal("ID-only Codex model acquired facts")
	}
	cursor, err := parseCursorCatalog(harnessCatalogFixture(t, "cursor.txt"))
	if err != nil || len(cursor) != 3 {
		t.Fatal(cursor, err)
	}
	if cursor[0].Model != "grok-4.7-high" || cursor[0].Effort != "" || cursor[0].Facts["native_levels"].Value != nil {
		t.Fatal("Cursor suffix became a claimed native effort")
	}
	opencode, err := parseOpenCodeCatalog(harnessCatalogFixture(t, "opencode.txt"))
	if err != nil || len(opencode) != 1 {
		t.Fatal(opencode, err)
	}
	if opencode[0].Model != "google/gemini-2.5-pro" || string(opencode[0].Facts["tools"].Value) != "true" || opencode[0].Facts["native_levels"].Value != nil {
		t.Fatal("OpenCode route/capability lost or variants invented levels")
	}
	claude, err := parseClaudeCatalog(harnessCatalogFixture(t, "claude.json"))
	if err != nil || len(claude) != 2 {
		t.Fatal(claude, err)
	}
	if string(claude[0].Facts["native_levels"].Value) != `["low","high"]` || len(claude[1].Facts) != 0 {
		t.Fatal("SDK native levels lost or unknown default invented")
	}
	if _, _, err := parseCodexCatalog([]byte(`{"data":[],"data":[]}`)); err == nil {
		t.Fatal("ambiguous Codex duplicate keys accepted")
	}
	if _, err := parseClaudeCatalog([]byte(`[{"value":"id"}]`)); err != nil {
		t.Fatal("valid ID-only SDK response rejected", err)
	}
	if _, err := parseClaudeCatalog([]byte(`[{"value":"id","value":"other"}]`)); err == nil {
		t.Fatal("ambiguous SDK duplicate keys accepted")
	}
	if modelreport.ValidateCatalog("fresh", []modelreport.CatalogModel{{Model: "id", Facts: map[string]modelreport.CatalogField{"credential": {Value: json.RawMessage("null"), Path: "credential"}}}}) == nil {
		t.Fatal("unallowlisted null field accepted")
	}
}

// Risk: a successful prefix or repeated cursor is reported as a complete list.
func TestCodexCatalogBoundedPagesKeepPartialHonest(t *testing.T) {
	first := harnessCatalogFixture(t, "codex.json")
	var page map[string]any
	if json.Unmarshal(first, &page) != nil {
		t.Fatal("fixture")
	}
	page["nextCursor"] = "second"
	first, _ = json.Marshal(page)
	calls := 0
	got := collectCodexCatalog(t.Context(), func(_ context.Context, method string, params any) (json.RawMessage, error) {
		calls++
		if method != "model/list" || params.(map[string]any)["includeHidden"] != true {
			t.Fatal("hidden models omitted")
		}
		if calls == 1 {
			return first, nil
		}
		return nil, errors.New("recorded second-page failure")
	})
	if calls != 2 || got.State != "limited" || len(got.Models) != 2 {
		t.Fatal("partial list reported as complete", got, calls)
	}
	got = collectCodexCatalog(t.Context(), func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"data":[],"nextCursor":"same"}`), nil
	})
	if got.State != "limited" {
		t.Fatal("cursor cycle became a complete empty list")
	}
	got = collectCodexCatalog(t.Context(), func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"data":[]}`), nil
	})
	if got.State != "fresh" || got.Models == nil || len(got.Models) != 0 {
		t.Fatal("known empty list became failure")
	}
}

// Risk: pi levels from its initially selected model are assigned to every model.
func TestPiCatalogReadsLevelsForConfirmedSelectedModel(t *testing.T) {
	sequence := []string{}
	got := collectPiCatalog(t.Context(), "anthropic", func(_ context.Context, method string, args any) (json.RawMessage, error) {
		sequence = append(sequence, method)
		switch method {
		case "get_available_models":
			return harnessCatalogFixture(t, "pi.json"), nil
		case "set_model":
			if !reflect.DeepEqual(args, map[string]any{"provider": "anthropic", "modelId": "claude-opus-5"}) {
				t.Fatal(args)
			}
			return json.RawMessage(`{"provider":"anthropic","id":"claude-opus-5"}`), nil
		case "get_available_thinking_levels":
			return harnessCatalogFixture(t, "pi-levels.json"), nil
		default:
			t.Fatal("prompt or unrelated command", method)
			return nil, nil
		}
	})
	if got.State != "fresh" || len(got.Models) != 1 || string(got.Models[0].Facts["native_levels"].Value) != `["off","minimal","low","medium","high"]` || !reflect.DeepEqual(sequence, []string{"get_available_models", "set_model", "get_available_thinking_levels"}) {
		t.Fatal(got, sequence)
	}
	got = collectPiCatalog(t.Context(), "anthropic", func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method == "get_available_models" {
			return harnessCatalogFixture(t, "pi.json"), nil
		}
		return json.RawMessage(`{"provider":"google","id":"gemini-2.5-pro"}`), nil
	})
	if got.State != "limited" || got.Models[0].Facts["native_levels"].Value != nil {
		t.Fatal("wrong selected model established levels", got)
	}
}

type catalogFixtureAPI struct {
	*fakeAPI
	requests []modelreport.CatalogProbe
	reports  []modelreport.CatalogReport
	claims   int
}

func (a *catalogFixtureAPI) ClaimCatalogProbes(context.Context, string, string, string) ([]modelreport.CatalogProbe, string, error) {
	a.claims++
	out := a.requests
	a.requests = nil
	return out, "", nil
}
func (a *catalogFixtureAPI) ReportCatalogProbe(_ context.Context, _ string, report modelreport.CatalogReport) error {
	a.reports = append(a.reports, report)
	return nil
}

type catalogBlockedClaimAPI struct {
	*catalogFixtureAPI
	entered chan struct{}
	release chan struct{}
}

func (a *catalogBlockedClaimAPI) ClaimCatalogProbes(ctx context.Context, daemon, generation, cursor string) ([]modelreport.CatalogProbe, string, error) {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-a.release:
		return a.catalogFixtureAPI.ClaimCatalogProbes(ctx, daemon, generation, cursor)
	}
}

// Risk: a slow catalog claim/capture holds queued runs and health polling on
// the dispatch path. Channels establish the overlap; deadlines only guard hangs.
func TestCatalogCaptureLoopDoesNotHoldDispatchOrHealthPolling(t *testing.T) {
	s, base, _ := testSupervisor(t)
	api := &catalogBlockedClaimAPI{catalogFixtureAPI: &catalogFixtureAPI{fakeAPI: base}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.api = api
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pollDone := make(chan error, 1)
	go func() { pollDone <- s.PollOnce(ctx) }()
	select {
	case <-api.entered:
		close(api.release)
		<-pollDone
		t.Fatal("dispatch poll entered the blocking catalog claim before starting queued work")
	case err := <-pollDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch poll did not finish")
	}
	if base.claims != 1 || api.claims != 0 {
		t.Fatal("queued run was not claimed independently of catalog discovery")
	}
	captureDone := make(chan struct{})
	go func() { defer close(captureDone); s.runCapacityCaptureTick(ctx) }()
	defer func() {
		cancel()
		select {
		case <-captureDone:
		case <-time.After(5 * time.Second):
			t.Error("catalog loop did not join cancellation")
		}
	}()
	select {
	case <-api.entered:
	case <-captureDone:
		t.Fatal("background capture tick omitted model catalog discovery")
	case <-time.After(5 * time.Second):
		t.Fatal("background capture tick did not reach catalog claim")
	}
	go func() { pollDone <- s.ProbeOnce(ctx) }()
	select {
	case err := <-pollDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked catalog claim held the health poll")
	}
	select {
	case <-captureDone:
		t.Fatal("catalog claim completed before its release barrier")
	default:
	}
	close(api.release)
	select {
	case <-captureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("background catalog capture did not join")
	}
	if api.claims != 1 {
		t.Fatal("background tick did not service exactly one catalog claim")
	}
}

// Risk: no queued run means no discovery, or scheduled discovery bypasses idle
// qualification and turns an unsupported operation into successful evidence.
func TestCatalogPollServicesIdleAccountsAndKeepsQualification(t *testing.T) {
	s, base, _ := testSupervisor(t)
	api := &catalogFixtureAPI{fakeAPI: base, requests: []modelreport.CatalogProbe{{AccountID: "account", AccountKey: "local", Harness: Codex, RequestID: modelreport.EvidenceID("request"), BindingRevision: 7}}}
	s.api = api
	if err := s.ProbeOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.runCapacityCaptureTick(t.Context())
	if got := s.captureCatalog(t.Context(), s.accounts[0], &CodexAdapter{}); got.State != "unsupported" {
		t.Fatal("unqualified idle Codex launch was enabled")
	}
	if api.claims < 1 || len(api.reports) != 1 || api.reports[0].State != "unsupported" || api.reports[0].BindingRevision != 7 || api.reports[0].Models == nil || len(api.reports[0].Models) != 0 || s.capacityCapturing {
		t.Fatal("idle safety/result contract lost", api.reports)
	}
	api.requests = []modelreport.CatalogProbe{{AccountID: "account", AccountKey: "different", Harness: Codex, RequestID: "replacement"}}
	s.captureModelCatalogs(t.Context())
	if len(api.reports) != 1 {
		t.Fatal("foreign local account key was probed")
	}
}
