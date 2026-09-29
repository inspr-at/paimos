// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
)

func usageSession(t *testing.T, f *harnessFixture, management string) (string, string, string) {
	t.Helper()
	lease := "synthetic-usage-generation-lease-" + uid()
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
		"management_mode": management, "role": "worker", "harness_session_ref": uid(), "worker_lease": lease,
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	return base + "/" + id, id, lease
}

func usagePayload() map[string]any {
	return map[string]any{"report_id": uid(), "model": "test-model", "sequence": 1, "input_tokens": 100, "output_tokens": 20,
		"cached_input_tokens": 40, "provisional": true, "billing_mode": "unknown"}
}

func usagePrice(t *testing.T, f *harnessFixture, version int, rate string) *httptest.ResponseRecorder {
	t.Helper()
	return f.call(f.person, "POST", "/api/model-prices", map[string]any{"model": "test-model", "version": version,
		"input_usd_per_million": rate, "output_usd_per_million": "10", "cached_input_usd_per_million": "0.25"}, "")
}

func usageResult(t *testing.T, w *httptest.ResponseRecorder) (harness.SessionModelUsage, bool) {
	t.Helper()
	expect(t, w, 200)
	var out struct {
		Usage    harness.SessionModelUsage `json:"usage"`
		Replayed bool                      `json:"replayed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Usage, out.Replayed
}

func TestUsageModelAndAccountLabelLimits(t *testing.T) {
	f := fixture(t)
	path, _, lease := usageSession(t, f, "unmanaged")
	model := strings.Repeat("m", 128)
	label := strings.Repeat("界", 128) // Limit is Unicode code points, as for session metadata.
	price := map[string]any{"model": model, "version": 1,
		"input_usd_per_million": "1", "output_usd_per_million": "1", "cached_input_usd_per_million": "1"}
	expect(t, f.call(f.person, "POST", "/api/model-prices", price, ""), 201)
	expect(t, f.call(f.person, "GET", "/api/model-prices?model="+model, nil, ""), 200)
	report := usagePayload()
	report["model"], report["account_label"], report["billing_mode"] = model, label, "api"
	out, _ := usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if out.Model != model || out.AccountLabel == nil || *out.AccountLabel != label || out.PriceVersion == nil || *out.PriceVersion != 1 {
		t.Fatalf("128-character model/account label or price was lost: %+v", out)
	}
	w := f.call(f.person, "GET", path+"/usage", nil, "")
	expect(t, w, 200)
	items := decode(t, w)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["account_label"] != label {
		t.Fatal("stored 128-character account label was not returned")
	}
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{"model 129", "model", model + "m"},
		{"model invalid character", "model", "valid@model"},
		{"account label 129 code points", "account_label", label + "界"},
		{"account label control", "account_label", "public\nlabel"},
		{"account label untrimmed", "account_label", " public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := usagePayload()
			invalid[tc.field] = tc.value
			expect(t, f.call(f.agent, "POST", path+"/usage", invalid, lease), 400)
		})
	}
	for _, invalidModel := range []string{model + "m", "valid@model"} {
		price["model"] = invalidModel
		expect(t, f.call(f.person, "POST", "/api/model-prices", price, ""), 400)
		expect(t, f.call(f.person, "GET", "/api/model-prices?model="+invalidModel, nil, ""), 400)
	}
}

func TestUsageReportingLifecycle(t *testing.T) {
	f := fixture(t)
	expect(t, usagePrice(t, f, 1, "2.5"), 201)
	expect(t, usagePrice(t, f, 1, "2.500000"), 201)
	expect(t, usagePrice(t, f, 1, "3"), 409)
	path, id, lease := usageSession(t, f, "unmanaged")
	w := f.call(f.person, "GET", path+"/usage", nil, "")
	expect(t, w, 200)
	initial := decode(t, w)
	if initial["reported"] != false || len(initial["items"].([]any)) != 0 {
		t.Fatal("unreported usage claimed measured totals")
	}
	report := usagePayload()
	report["billing_mode"], report["subscription_label"], report["account_label"] = "subscription", "Synthetic plan", "Synthetic account"
	out, replay := usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if replay || out.EstimatedCostUSD != nil || out.PriceVersion != nil || !out.Provisional || out.BillingMode != "subscription" || out.MetadataSource != "reported" {
		t.Fatalf("unexpected usage %+v", out)
	}
	first := make(map[string]any)
	for k, v := range report {
		first[k] = v
	}
	_, replay = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if !replay {
		t.Fatal("retry not recognized")
	}
	report["input_tokens"] = 101
	expect(t, f.call(f.agent, "POST", path+"/usage", report, lease), 409)
	report["report_id"], report["sequence"], report["input_tokens"] = uid(), 2, 150
	expect(t, usagePrice(t, f, 3, "99"), 201)
	expect(t, usagePrice(t, f, 2, "20"), 409)
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if out.EstimatedCostUSD != nil || out.PriceVersion != nil || out.BillingMode != "subscription" {
		t.Fatalf("subscription priced: %+v", out)
	}
	out, replay = usageResult(t, f.call(f.agent, "POST", path+"/usage", first, lease))
	if !replay || out.Sequence != 2 || *out.InputTokens != 150 {
		t.Fatal("old exact replay did not return latest row")
	}
	// Final reporting after stop needs the same generation proof, but not liveness.
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	report["report_id"], report["sequence"], report["provisional"] = uid(), 3, false
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if out.Provisional {
		t.Fatal("final report remained provisional")
	}
	_, replay = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if !replay {
		t.Fatal("final retry not recognized")
	}
	report["report_id"], report["sequence"], report["input_tokens"] = uid(), 4, 151
	expect(t, f.call(f.agent, "POST", path+"/usage", report, lease), 409)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var rows, receipts, eventCount int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_session_usage WHERE session_id=$1`, id).Scan(&rows); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_usage_receipts WHERE session_id=$1`, id).Scan(&receipts); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.usage_reported' AND after->>'session_id'=$1`, id).Scan(&eventCount); err != nil {
			return err
		}
		if rows != 1 || receipts != 3 || eventCount != 3 {
			t.Fatalf("rows=%d receipts=%d events=%d", rows, receipts, eventCount)
		}
		return nil
	})
	// A new managed session pins the latest version; no managed telemetry is duplicated.
	managed, _, managedLease := usageSession(t, f, "managed")
	managedReport := usagePayload()
	managedReport["billing_mode"] = "api"
	out, _ = usageResult(t, f.call(f.agent, "POST", managed+"/usage", managedReport, managedLease))
	if out.PriceVersion == nil || *out.PriceVersion != 3 || out.EstimatedCostUSD == nil || *out.EstimatedCostUSD != "0.006150000000" {
		t.Fatalf("managed pricing: %+v", out)
	}
	expect(t, f.call(f.agent, "GET", managed+"/usage", nil, ""), 200)
	prices := decode(t, f.call(f.person, "GET", "/api/model-prices?model=test-model", nil, ""))["items"].([]any)
	if len(prices) != 1 || prices[0].(map[string]any)["version"] != float64(3) {
		t.Fatalf("latest price %v", prices)
	}
}

func TestUsageUnknownAndLatePricing(t *testing.T) {
	f := fixture(t)
	path, _, lease := usageSession(t, f, "managed")
	report := usagePayload()
	report["cached_input_tokens"], report["provisional"] = nil, false
	out, _ := usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if !out.Provisional || out.EstimatedCostUSD != nil || out.PriceVersion != nil || out.CachedInputTokens != nil || out.CostStatus != "unknown" {
		t.Fatalf("unknown truth: %+v", out)
	}
	report["report_id"], report["sequence"], report["cached_input_tokens"] = uid(), 2, 40
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if out.Provisional || out.EstimatedCostUSD != nil {
		t.Fatalf("unpriced final truth: %+v", out)
	}
	expect(t, usagePrice(t, f, 1, "2.5"), 201)
	report["report_id"], report["sequence"] = uid(), 3
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if out.EstimatedCostUSD != nil || out.PriceVersion != nil {
		t.Fatal("unknown billing received a dollar estimate")
	}
	apiPath, _, apiLease := usageSession(t, f, "managed")
	apiReport := usagePayload()
	apiReport["billing_mode"] = "api"
	out, _ = usageResult(t, f.call(f.agent, "POST", apiPath+"/usage", apiReport, apiLease))
	if out.EstimatedCostUSD == nil || *out.EstimatedCostUSD != "0.000360000000" || out.PriceVersion == nil || *out.PriceVersion != 1 {
		t.Fatalf("api pricing missing: %+v", out)
	}
	report["report_id"], report["model"], report["sequence"] = uid(), "second-model", 1
	report["input_tokens"], report["output_tokens"], report["cached_input_tokens"] = 0, 0, 0
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
	if out.EstimatedCostUSD != nil || out.Provisional || *out.InputTokens != 0 {
		t.Fatal("measured zero with unknown price lost")
	}
	w := f.call(f.person, "GET", path+"/usage", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 2 {
		t.Fatal("model rows were combined")
	}
}

func TestUsageRejectsInvalidAndUnauthorized(t *testing.T) {
	f := fixture(t)
	path, _, lease := usageSession(t, f, "unmanaged")
	for _, tc := range []struct {
		name  string
		key   string
		value any
	}{
		{"negative", "input_tokens", -1}, {"too large", "output_tokens", int64(1000000000001)},
		{"fraction", "input_tokens", 1.5}, {"string", "input_tokens", "100"},
		{"cache exceeds input", "cached_input_tokens", 101}, {"sequence zero", "sequence", 0},
		{"missing provisional", "provisional", nil}, {"billing absent", "billing_mode", nil},
		{"foreign account", "account_id", uid()}, {"private field", "transcript", "synthetic forbidden text"},
		{"model blank", "model", ""}, {"bad label", "account_label", "line\nfeed"},
		{"subscription mismatch", "subscription_label", "Synthetic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := usagePayload()
			in[tc.key] = tc.value
			expect(t, f.call(f.agent, "POST", path+"/usage", in, lease), 400)
		})
	}
	expect(t, f.call(f.person, "POST", path+"/usage", usagePayload(), lease), 403)
	expect(t, f.call(f.agent, "POST", path+"/usage", usagePayload(), "wrong-synthetic-lease-00000000000000000"), 403)
	expect(t, f.call(f.foreign, "GET", path+"/usage", nil, ""), 404)
	expect(t, f.call(f.agent, "POST", strings.Replace(path, f.project, "invalid", 1)+"/usage", usagePayload(), lease), 400)
	// Price management is checked inside the handler as well as the production route map.
	expect(t, f.call(f.agent, "POST", "/api/model-prices", map[string]any{}, ""), 403)
	dbtestPerson := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','No project access')`, dbtestPerson.TenantID, dbtestPerson.ID)
		return err
	})
	expect(t, f.call(dbtestPerson, "GET", path+"/usage", nil, ""), 404)
	expect(t, f.call(dbtestPerson, "POST", "/api/model-prices", map[string]any{}, ""), 403)
	in := usagePayload()
	usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	for _, principal := range []tenant.Principal{f.foreign, dbtestPerson} {
		err := db.InTenant(tenant.WithPrincipal(context.Background(), principal), f.db.App, principal.TenantID, func(tx pgx.Tx) error {
			for _, table := range []string{"harness_session_usage", "harness_usage_receipts"} {
				var n int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Fatalf("%s leaked %d rows", table, n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The exact key loses its worker scope, so proof alone cannot report.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.read'] WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/usage", in, lease), 403)
}

func TestUsageConcurrentCumulativeReports(t *testing.T) {
	f := fixture(t)
	path, id, lease := usageSession(t, f, "managed")
	in := usagePayload()
	const workers = 8
	responses := make(chan *httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- f.call(f.agent, "POST", path+"/usage", in, lease) }()
	}
	wg.Wait()
	close(responses)
	accepted := 0
	for w := range responses {
		out, replayed := usageResult(t, w)
		if !replayed {
			accepted++
		}
		if *out.InputTokens != 100 {
			t.Fatal("concurrent retry double-counted")
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d reports", accepted)
	}
	// Two different receipts compete for the same next sequence: exactly one wins.
	responses = make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		next := usagePayload()
		next["sequence"] = 2
		next["input_tokens"] = 200
		wg.Add(1)
		go func() { defer wg.Done(); responses <- f.call(f.agent, "POST", path+"/usage", next, lease) }()
	}
	wg.Wait()
	close(responses)
	accepted = 0
	conflicts := 0
	for w := range responses {
		if w.Code == 200 {
			accepted++
		} else if w.Code == 409 {
			conflicts++
		} else {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("accepted=%d conflicts=%d", accepted, conflicts)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var total, receipts int64
		if err := tx.QueryRow(t.Context(), `SELECT sum(input_tokens) FROM harness_session_usage WHERE session_id=$1`, id).Scan(&total); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_usage_receipts WHERE session_id=$1`, id).Scan(&receipts); err != nil {
			return err
		}
		if total != 200 || receipts != 2 {
			t.Fatalf("total=%d receipts=%d", total, receipts)
		}
		return nil
	})
}

func TestUsageRejectsMalformedJSON(t *testing.T) {
	f := fixture(t)
	path, _, lease := usageSession(t, f, "unmanaged")
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"input_tokens":9223372036854775808}`} {
		req := httptest.NewRequest("POST", path+"/usage", bytes.NewBufferString(body)).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
		req.Header.Set("Authorization", "Bearer "+f.key)
		req.Header.Set("X-Aeon-Worker-Lease", lease)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		expect(t, w, http.StatusBadRequest)
	}
}

func TestUsageMonotonicHTTPAndAccountMetadata(t *testing.T) {
	f := fixture(t)
	path, _, lease := usageSession(t, f, "unmanaged")
	accountID := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2,'synthetic-account','codex','synthetic-daemon',$3,'Synthetic account')`, f.person.TenantID, accountID, f.agent.ID)
		return err
	})
	in := usagePayload()
	in["account_id"] = accountID
	in["billing_mode"] = "subscription"
	in["subscription_label"] = "Synthetic plan"
	out, _ := usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	if out.AccountID == nil || *out.AccountID != accountID || out.AccountLabel != nil || out.EstimatedCostUSD != nil {
		t.Fatalf("metadata inferred or lost: %+v", out)
	}
	for _, change := range []struct {
		key   string
		value any
	}{{"input_tokens", 99}, {"output_tokens", 19}, {"cached_input_tokens", 39}, {"cached_input_tokens", 41}, {"output_tokens", nil}, {"sequence", 1}} {
		next := map[string]any{}
		for k, v := range in {
			next[k] = v
		}
		next["report_id"], next["sequence"] = uid(), 2
		next[change.key] = change.value
		expect(t, f.call(f.agent, "POST", path+"/usage", next, lease), 409)
	}
	in["report_id"], in["sequence"], in["input_tokens"], in["cached_input_tokens"] = uid(), 2, 200, 90
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	if *out.InputTokens != 200 || *out.CachedInputTokens != 90 {
		t.Fatal("monotonic cumulative growth rejected")
	}
}

func TestUsageReasoningRoundTrip(t *testing.T) {
	f := fixture(t)
	path, _, lease := usageSession(t, f, "unmanaged")
	in := usagePayload()
	in["reasoning_tokens"] = 6
	out, _ := usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	if out.ReasoningTokens == nil || *out.ReasoningTokens != 6 || out.EstimatedCostUSD != nil {
		t.Fatalf("reasoning: %+v", out)
	}
	down := map[string]any{}
	for k, v := range in {
		down[k] = v
	}
	down["report_id"], down["sequence"], down["reasoning_tokens"] = uid(), 2, 5
	expect(t, f.call(f.agent, "POST", path+"/usage", down, lease), 409)
	in["report_id"], in["sequence"], in["reasoning_tokens"] = uid(), 2, 7
	out, _ = usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	if out.ReasoningTokens == nil || *out.ReasoningTokens != 7 {
		t.Fatalf("growth: %+v", out)
	}
	over := usagePayload()
	over["sequence"], over["reasoning_tokens"] = 3, 21
	expect(t, f.call(f.agent, "POST", path+"/usage", over, lease), 400)
}

func TestUsagePricingIsolationAndStorageConstraints(t *testing.T) {
	f := fixture(t)
	expect(t, usagePrice(t, f, 1, "2.5"), 201)
	w := f.call(f.foreign, "GET", "/api/model-prices", nil, "")
	expect(t, w, 200)
	items := decode(t, w)["items"].([]any)
	if len(items) == 0 {
		t.Fatal("seeded api list prices missing")
	}
	seeded := map[string]map[string]any{}
	for _, item := range items {
		row := item.(map[string]any)
		model := row["model"].(string)
		if model == "test-model" {
			t.Fatal("foreign tenant price leaked")
		}
		seeded[model] = row
	}
	for _, absent := range []string{"haiku", "sonnet", "opus", "fable", "composer-2.5", "grok-4", "grok-4-fast", "gpt-6-terra", "grok-4.7-high"} {
		if _, ok := seeded[absent]; ok {
			t.Fatalf("unsourced model %s was seeded", absent)
		}
	}
	for model, want := range map[string][3]string{
		"grok-4.7":                  {"2.000000", "0.500000", "6.000000"},
		"claude-sonnet-5":           {"2.000000", "0.200000", "10.000000"},
		"anthropic/claude-sonnet-5": {"2.000000", "0.200000", "10.000000"},
		"claude-opus-5":             {"5.000000", "0.500000", "25.000000"},
		"anthropic/claude-opus-5":   {"5.000000", "0.500000", "25.000000"},
		"claude-opus-5-5":           {"4.000000", "0.200000", "20.000000"},
		"claude-fable-5-1":          {"10.000000", "0.250000", "50.000000"},
		"claude-haiku-4-5-20251001": {"1.000000", "0.100000", "5.000000"},
		"gpt-6-sol":                 {"2.000000", "0.200000", "10.000000"},
		"gpt-6-luna":                {"0.100000", "0.010000", "0.500000"},
		"gpt-4.1":                   {"2.000000", "0.500000", "8.000000"},
	} {
		row := seeded[model]
		if row == nil || row["input_usd_per_million"] != want[0] || row["cached_input_usd_per_million"] != want[1] || row["output_usd_per_million"] != want[2] {
			t.Fatalf("seed %s = %+v", model, row)
		}
	}
	path, id, lease := usageSession(t, f, "managed")
	in := usagePayload()
	usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	for _, sql := range []string{
		`UPDATE model_prices SET input_usd_per_million=3`,
		`DELETE FROM model_prices`,
		`UPDATE harness_usage_receipts SET sequence=9`,
		`DELETE FROM harness_usage_receipts`,
		`UPDATE harness_session_usage SET input_tokens=-1`,
		`UPDATE harness_session_usage SET output_tokens=1000000000001`,
		`UPDATE harness_session_usage SET cached_input_tokens=101`,
	} {
		t.Run(sql, func(t *testing.T) {
			err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.db.App, f.person.TenantID, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), sql); return err })
			if err == nil {
				t.Fatal("storage constraint accepted invalid mutation")
			}
		})
	}
	// Maximum values survive exact NUMERIC persistence without int64 products.
	p := map[string]any{"model": "max-model", "version": 1, "input_usd_per_million": "1000000", "output_usd_per_million": "1000000", "cached_input_usd_per_million": "1000000"}
	expect(t, f.call(f.person, "POST", "/api/model-prices", p, ""), 201)
	in["model"], in["report_id"], in["billing_mode"], in["input_tokens"], in["output_tokens"], in["cached_input_tokens"] = "max-model", uid(), "api", int64(1000000000000), int64(1000000000000), int64(1000000000000)
	out, _ := usageResult(t, f.call(f.agent, "POST", path+"/usage", in, lease))
	if *out.EstimatedCostUSD != "2000000000000.000000000000" {
		t.Fatal("maximum exact cost changed")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM run_telemetry WHERE run_id IN (SELECT run_id FROM harness_sessions WHERE id=$1)`, id).Scan(&n)
		if n != 0 {
			t.Fatal("session reports created overlapping managed telemetry")
		}
		return err
	})
}
