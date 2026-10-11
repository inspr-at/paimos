// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: probe publication overlaps retirement and panics when mounting the
// production router, or a routing fallback admits unrelated model operations.
func TestHarnessProbeRoutesMountWithoutRetirementConflict(t *testing.T) {
	mux := http.NewServeMux()
	(&Module{}).Mount(mux)
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	agent := tenant.Principal{ID: "agent", TenantID: "tenant", Kind: tenant.Agent}
	const account = "11111111-1111-4111-8111-111111111111"
	report := `{"request_id":"22222222-2222-4222-8222-222222222222","binding_revision":1,"daemon_id":"fixture","daemon_generation":"generation","state":"fresh","models":[]}`
	for _, tc := range []struct {
		name, path, pattern, body string
		principal                 tenant.Principal
		status                    int
		error                     string
	}{
		{"claim", "/api/models/harness-probes/claim", "POST /api/models/harness-probes/claim", `{"daemon_id":"fixture","daemon_generation":"generation"}`, person, 403, "agent required"},
		{"report", "/api/models/harness-probes/" + account, "POST /api/models/{collection}/{accountId}", report, person, 403, "agent required"},
		{"retirement", "/api/models/" + account + "/retire", "POST /api/models/{id}/retire", `{}`, agent, 403, "person_required"},
		{"overlap", "/api/models/harness-probes/retire", "POST /api/models/{id}/retire", `{}`, person, 400, "invalid_profile_id"},
		{"invalid account", "/api/models/harness-probes/not-a-uuid", "POST /api/models/{collection}/{accountId}", report, person, 400, "invalid probe binding"},
		{"unrelated collection", "/api/models/other/" + account, "POST /api/models/{collection}/{accountId}", report, person, 404, ""},
		{"unlisted action", "/api/models/" + account + "/other", "POST /api/models/{collection}/{accountId}", report, person, 404, ""},
		{"extra segment", "/api/models/harness-probes/" + account + "/extra", "", report, person, 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r = r.WithContext(tenant.WithPrincipal(r.Context(), tc.principal))
			if _, pattern := mux.Handler(r); pattern != tc.pattern {
				t.Fatalf("matched %q, want %q", pattern, tc.pattern)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.error != "" {
				var response struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error != tc.error {
					t.Fatalf("wrong rejection: %s, want %q: %v", w.Body.String(), tc.error, err)
				}
			}
		})
	}
}

// Risk: an ID-only Cursor probe splits a route from its recorded launch tuple,
// or suffix guessing folds a route for which no effort has ever been recorded.
func TestCursorHarnessProbeReusesRecordedLaunchIdentity(t *testing.T) {
	for _, withPin := range []bool{true, false} {
		name := "observation"
		if withPin {
			name = "pin"
		}
		t.Run(name, func(t *testing.T) {
			reset(t)
			owner := makePrincipal(t, "cursor-probe-identity", "person", "Owner", []string{"admin"})
			agent := addPrincipal(t, owner.TenantID, "agent", "Daemon", nil)
			grantModelReporter(t, owner, agent)
			agent.Scopes = []string{"models.read", "models.report"}
			decode[catalogPage](t, &agent, "GET", "/api/models/catalog", "", 200)
			// Use mounted production handlers without decode's legacy pin setup:
			// the observation-only case must remain free of launch profiles.
			send := func(path string, body any, out any) {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				code, response := call(t, &agent, "POST", path, string(raw))
				if code != 200 || json.Unmarshal(response, out) != nil {
					t.Fatalf("%s: HTTP %d: %s", path, code, response)
				}
			}
			account := enrollEvidenceHarness(t, owner, agent, "cursor")
			model, effort := "grok-4.7-xhigh", "xhigh"
			inRegistry(t, owner, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['models.read','models.report'] WHERE principal_id=$1`, agent.ID); err != nil {
					return err
				}
				// A daemon generation is recorded with its probe outcome, as the
				// readiness writer does; agent_accounts_check1 pairs the two.
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=now(),last_probe_ok=true,last_daemon_generation='generation' WHERE id=$1`, account); err != nil {
					return err
				}
				// Turn off automatic pins through the canonical account-use rule
				// writer sequence; 1309 maps auto_add_profiles=false to shipped_only.
				if _, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models='shipped_only',revision=revision+1`); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.account_use_rule_write','on',true)`); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET auto_add_profiles=false`); err != nil {
					return err
				}
				if withPin {
					_, err := insertObservedProfile(t.Context(), tx, owner.TenantID, profileWrite{Slug: "cursor-probe-pin", Version: "fixture", Harness: "cursor", Family: "xai", Model: model, Effort: effort, Tier: "standard", Source: "harness"})
					return err
				}
				return nil
			})
			seedEvidenceSession(t, owner, agent, "cursor", model, effort)
			reportRunStart := func() {
				t.Helper()
				var got ReportResult
				send("/api/models/reports", []Observation{{ReportID: EvidenceID("cursor-run-start"), Harness: "cursor", Model: model, Effort: effort, Status: "working"}}, &got)
				if got.Recorded != 1 {
					t.Fatal("run-start report was not recorded", got)
				}
			}
			if !withPin {
				reportRunStart()
			}
			inRegistry(t, owner, func(tx pgx.Tx) error {
				var pins int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_profiles WHERE harness='cursor' AND model=$1`, model).Scan(&pins); err != nil {
					return err
				}
				if withPin && pins != 1 || !withPin && pins != 0 {
					t.Fatal("fixture did not isolate pin and observation identity sources", pins)
				}
				return nil
			})
			var claim struct {
				Items []modelreport.CatalogProbe `json:"items"`
			}
			send("/api/models/harness-probes/claim", map[string]string{"daemon_id": "fixture", "daemon_generation": "generation"}, &claim)
			if len(claim.Items) != 1 || claim.Items[0].AccountID != account {
				t.Fatal("Cursor probe was not claimed", claim)
			}
			unknown := "grok-4.7-high"
			report := modelreport.CatalogReport{RequestID: claim.Items[0].RequestID, BindingRevision: claim.Items[0].BindingRevision, DaemonID: "fixture", Generation: "generation", State: "fresh", Models: []modelreport.CatalogModel{
				{Model: model, Facts: map[string]modelreport.CatalogField{"display_name": {Value: json.RawMessage(`"Grok native fixture"`), Path: "models[].name"}}},
				{Model: unknown, Facts: map[string]modelreport.CatalogField{}},
			}}
			var result map[string]any
			send("/api/models/harness-probes/"+account, report, &result)
			if result["recorded"] != true {
				t.Fatal("probe publication was not recorded")
			}
			if withPin {
				reportRunStart()
			}
			page := decode[catalogPage](t, &owner, "GET", "/api/models/catalog?limit=50", "", 200)
			if page.NextCursor != "" || page.Truncated {
				t.Fatal("fixture catalog read was incomplete")
			}
			for _, tc := range []struct{ model, identity string }{{model, "xai:grok-4.7"}, {unknown, "xai:" + unknown}} {
				identities, native, runStart := 0, false, false
				for _, entry := range page.Models {
					for _, route := range entry.Routes {
						if route.Route != "cursor/"+tc.model {
							continue
						}
						identities++
						if entry.Identity != tc.identity {
							t.Errorf("route %s appeared under %s, want %s", route.Route, entry.Identity, tc.identity)
						}
						if tc.model == model {
							f := route.Facts["display_name"]
							native = f.Source == "harness:cursor:"+account && f.FieldPath == "models[].name" && string(f.Value) == `"Grok native fixture"`
							for _, f := range entry.Evidence {
								if f.Route == route.Route && f.Source == "agent-report:cursor" && f.Fact == "observed_level.xhigh" {
									runStart = true
								}
							}
						}
					}
				}
				if identities != 1 || tc.model == model && (!native || !runStart) {
					t.Errorf("route %s split or lost probe/run-start facts: identities=%d native=%v run-start=%v", tc.model, identities, native, runStart)
				}
			}
		})
	}
}

// Risk: scheduled/manual probes publish stale account authority, partial lists
// prove disappearance, or native harness facts grant/reorder existing routes.
func TestHarnessProbesScheduleCheckNowAndFencePublication(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "harness-probes", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Daemon", nil)
	grantModelReporter(t, owner, agent)
	agent.Scopes = []string{"models.read", "models.report"}
	if code, raw := call(t, &agent, "GET", "/api/models/catalog", ""); code != 200 {
		t.Fatal(code, string(raw))
	}
	account := enrollEvidenceHarness(t, owner, agent, "codex")
	var now time.Time
	inRegistry(t, owner, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT now()`).Scan(&now); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['models.read','models.report'] WHERE principal_id=$1`, agent.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=$3,last_probe_ok=true,last_daemon_generation='generation',capacity_owner=$2 WHERE id=$1`, account, owner.ID, now)
		return err
	})
	m := &Module{pool: appPool, discovery: fixtureCatalogClient(t, nil), validationClock: func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }}
	mux := http.NewServeMux()
	m.Mount(mux)
	send := func(p tenant.Principal, path string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		if p.Kind == tenant.Agent {
			r.Header.Set("Authorization", "Bearer aeon_"+strings.ReplaceAll(p.ID, "-", "")+"_model-read-fixture")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	claim := func() []modelreport.CatalogProbe {
		t.Helper()
		var out struct {
			Items []modelreport.CatalogProbe `json:"items"`
			Next  string                     `json:"next_cursor"`
		}
		if err := json.Unmarshal(send(agent, "/api/models/harness-probes/claim", map[string]string{"daemon_id": "fixture", "daemon_generation": "generation"}, 200), &out); err != nil {
			t.Fatal(err)
		}
		if out.Next != "" {
			t.Fatal("unexpected fixture truncation")
		}
		return out.Items
	}
	jobs := claim()
	if len(jobs) != 1 || jobs[0].AccountID != account || jobs[0].Harness != "codex" {
		t.Fatal("schedule needs no run or manual refresh", jobs)
	}
	if jobs := claim(); len(jobs) != 0 {
		t.Fatal("live lease duplicated", jobs)
	}
	request := jobs[0]
	model := modelreport.CatalogModel{Model: "remote-only-0", Facts: map[string]modelreport.CatalogField{
		"native_levels": {Value: json.RawMessage(`["max","low","focused"]`), Path: "supportedReasoningEfforts[].reasoningEffort"},
		"default_level": {Value: json.RawMessage(`"max"`), Path: "defaultReasoningEffort"},
		"hidden":        {Value: json.RawMessage("true"), Path: "hidden"},
		"successor":     {Value: json.RawMessage(`"replacement-model"`), Path: "upgrade"},
	}}
	idOnly := modelreport.CatalogModel{Model: "catalog-id-only", Facts: map[string]modelreport.CatalogField{}}
	report := modelreport.CatalogReport{RequestID: request.RequestID, BindingRevision: request.BindingRevision, DaemonID: "fixture", Generation: "generation", State: "fresh", Models: []modelreport.CatalogModel{model, idOnly}}
	path := "/api/models/harness-probes/" + account
	before := registryRoutes(t, owner)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		return RecordCatalogFacts(t.Context(), tx, owner.TenantID, []CatalogFact{
			{Provider: "openai", ModelID: model.Model, Fact: "native_levels", Value: json.RawMessage(`["high"]`), Source: "native-fixture", SourceKind: "native", FieldPath: "levels", FetchedAt: now.Add(time.Hour)},
			{Provider: "openai", ModelID: model.Model, Fact: "native_levels", Value: json.RawMessage(`["medium"]`), Source: "aggregator-fixture", SourceKind: "aggregator", FieldPath: "levels", FetchedAt: now.Add(2 * time.Hour)},
		})
	})
	send(agent, path, report, 200)
	send(agent, path, report, 200)
	if eventCount(t, owner, "model.harness_probed") != 1 || !routesEqual(before, registryRoutes(t, owner)) {
		t.Fatal("replay appended events or probe changed routes")
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var discovered, active int
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE enabled) FROM model_profiles WHERE harness='codex' AND model=$1 AND display_overrides->>'origin'='harness'`, model.Model).Scan(&discovered, &active); err != nil {
			return err
		}
		if discovered != 3 || active != 0 {
			t.Fatal("native discoveries were lost or activated", discovered, active)
		}
		var guessed int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_profiles WHERE model=$1`, idOnly.Model).Scan(&guessed); err != nil {
			return err
		}
		if guessed != 0 {
			t.Fatal("ID-only catalog entry acquired a guessed profile")
		}
		rows, err := tx.Query(t.Context(), `SELECT provider,model_id,route,fact,value,source,source_kind,field_path,fetched_at FROM model_catalog_facts WHERE model_id=$1`, model.Model)
		if err != nil {
			return err
		}
		defer rows.Close()
		facts := []CatalogFact{}
		for rows.Next() {
			var f CatalogFact
			if err = rows.Scan(&f.Provider, &f.ModelID, &f.Route, &f.Fact, &f.Value, &f.Source, &f.SourceKind, &f.FieldPath, &f.FetchedAt); err != nil {
				return err
			}
			facts = append(facts, f)
		}
		chosen := ResolveCatalogFacts(facts, "codex/"+model.Model)
		// jsonb re-renders arrays with spaces; compare the decoded native list.
		var levels []string
		if json.Unmarshal(chosen["native_levels"].Value, &levels) != nil || !slices.Equal(levels, []string{"max", "low", "focused"}) || chosen["native_levels"].SourceKind != "harness" || chosen["default_level"].FieldPath != "defaultReasoningEffort" {
			t.Fatal("harness facts did not outrank newer providers", chosen)
		}
		if chosen := ResolveCatalogFacts(facts, "pi/"+model.Model); string(chosen["native_levels"].Value) != `["high"]` {
			t.Fatal("harness facts escaped their route", chosen)
		}
		return rows.Err()
	})
	if jobs := claim(); len(jobs) != 0 {
		t.Fatal("completed probe bypassed interval", jobs)
	}
	now = now.Add(5 * time.Minute)
	var refresh RefreshResult
	if err := json.Unmarshal(send(owner, "/api/models/refresh", map[string]any{}, 200), &refresh); err != nil {
		t.Fatal(err)
	}
	if refresh.HarnessProbesQueued != 1 || refresh.HarnessProbesTruncated {
		t.Fatal("Check now did not queue harness discovery", refresh)
	}
	jobs = claim()
	if len(jobs) != 1 || jobs[0].RequestID == request.RequestID {
		t.Fatal("Check now did not bypass interval", jobs)
	}
	send(agent, path, report, 409) // Previous valid evidence may not complete a new request.
	report.RequestID = jobs[0].RequestID
	report.State = "limited"
	report.Models = []modelreport.CatalogModel{{Model: model.Model, Facts: map[string]modelreport.CatalogField{}}}
	send(agent, path, report, 200)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var listed bool
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT value FROM model_catalog_facts WHERE source=$1 AND route=$2 AND fact='listed'`, "harness:codex:"+account, "codex/"+idOnly.Model).Scan(&listed); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_catalog_facts WHERE source=$1 AND fact='native_levels'`, "harness:codex:"+account).Scan(&count); err != nil {
			return err
		}
		if !listed || count != 1 {
			t.Fatal("partial list destroyed last good evidence or proved disappearance")
		}
		return nil
	})
	// Each mutation below happens after a valid claim, before the final write.
	for _, test := range []struct {
		name, sql string
		want      int
	}{
		{"revision", `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`, 409},
		{"generation", `UPDATE agent_accounts SET last_daemon_generation='replaced' WHERE id=$1`, 409},
		{"owner", `UPDATE agent_accounts SET registered_by_principal_id=$2 WHERE id=$1`, 409},
		{"archived", `UPDATE agent_accounts SET archived_at=now() WHERE id=$1`, 409},
		{"reporting", `UPDATE model_refresh_settings SET agent_reports_enabled=false`, 403},
		{"scope", `UPDATE agent_keys SET scopes=ARRAY['models.read'] WHERE principal_id=$3`, 403},
		{"key", `UPDATE agent_keys SET revoked_at=now() WHERE principal_id=$3`, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRegistry(t, owner, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET registered_by_principal_id=$2,last_daemon_generation='generation',link_revision=$3,archived_at=NULL WHERE id=$1`, account, agent.ID, request.BindingRevision); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET agent_reports_enabled=true`); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=NULL,scopes=ARRAY['models.read','models.report'] WHERE principal_id=$1`, agent.ID); err != nil {
					return err
				}
				now = now.Add(time.Minute)
				return queueHarnessProbe(t.Context(), tx, owner.TenantID, account, request.BindingRevision, now)
			})
			jobs := claim()
			if len(jobs) != 1 {
				t.Fatal("fixture claim", jobs)
			}
			report.RequestID = jobs[0].RequestID
			report.State = "fresh"
			report.Models = []modelreport.CatalogModel{model}
			inRegistry(t, owner, func(tx pgx.Tx) error {
				// Parameters must follow the actual query: PostgreSQL rejects extras.
				var err error
				switch test.name {
				case "owner":
					_, err = tx.Exec(t.Context(), test.sql, account, owner.ID)
				case "reporting":
					_, err = tx.Exec(t.Context(), test.sql)
				case "scope", "key":
					_, err = tx.Exec(t.Context(), strings.ReplaceAll(test.sql, "$3", "$1"), agent.ID)
				default:
					_, err = tx.Exec(t.Context(), test.sql, account)
				}
				return err
			})
			beforeEvents := eventCount(t, owner, "model.harness_probed")
			send(agent, path, report, test.want)
			if eventCount(t, owner, "model.harness_probed") != beforeEvents {
				t.Fatal("denied result was published")
			}
		})
	}
}
