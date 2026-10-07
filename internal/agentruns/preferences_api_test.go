// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type prefsEvidence struct{ account string }

// Person writes carry the canonical identity displayed by this fixture.
func (f *fixture) prefsCall(t *testing.T, p tenant.Principal, body any, status int, dst any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "/api/model-preferences/profile?for=me", strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	r.Header.Set("If-Prefs-Person", f.person.ID)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("person preferences: got %d want %d: %s", w.Code, status, w.Body.String())
	}
	if dst != nil {
		if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
			t.Fatal(err)
		}
	}
}

func (e prefsEvidence) ResidencyClass(_ context.Context, _ pgx.Tx, a agentaccounts.Account, _ string) (string, string, *time.Time, error) {
	expires := time.Now().Add(time.Hour)
	class := "any"
	if a.ID == e.account {
		class = "eu"
	}
	return class, "fixture-proof", &expires, nil
}
func TestAliasResidencyOverHTTPAndActiveRunRestamp(t *testing.T) {
	f := setup(t)
	f.tx(t, f.person, func(tx pgx.Tx) error { return modelprefs.SeedKinds(t.Context(), tx, f.person.TenantID) })
	modelregistry.New(f.d.App).Mount(f.mux)
	agentaccounts.New(f.d.App).Mount(f.mux)
	alias := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name,linked_to) VALUES($1,$2,'person','Alias',$3)`, f.person.TenantID, alias.ID, f.person.ID)
		return err
	})
	f.prefsCall(t, alias, map[string]any{"revision": 0, "residency": "eu"}, 200, nil)
	var aliasDoc, canonicalDoc struct {
		PersonID string          `json:"person_id"`
		Profile  json.RawMessage `json:"profile"`
	}
	f.call(t, alias, "GET", "/api/model-preferences/board", nil, 200, &aliasDoc)
	f.call(t, f.person, "GET", "/api/model-preferences/board", nil, 200, &canonicalDoc)
	if aliasDoc.PersonID != f.person.ID || canonicalDoc.PersonID != f.person.ID || string(aliasDoc.Profile) != string(canonicalDoc.Profile) {
		t.Fatal("alias selected a different You slice")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var person, actor string
		if err := tx.QueryRow(t.Context(), `SELECT person_id::text,set_by::text FROM model_pref_profiles WHERE scope='person'`).Scan(&person, &actor); err != nil {
			return err
		}
		if person != f.person.ID || actor != alias.ID {
			t.Fatal("canonical scope / session audit actor", person, actor)
		}
		return nil
	})
	cloud := f.queueAccount(t, 1000000)
	o := f.order(t, nil)
	var run agentruns.Run
	f.call(t, alias, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 201, &run)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var person, residency string
		if err := tx.QueryRow(t.Context(), `SELECT prefs_person_id::text,residency FROM agent_runs WHERE id=$1`, run.ID).Scan(&person, &residency); err != nil {
			return err
		}
		if person != f.person.ID || residency != "eu" {
			t.Fatal("alias run stamp", person, residency)
		}
		wait, err := agentaccounts.WaitForRun(t.Context(), tx, run.ID)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "residency" {
			t.Fatal("unqualified route did not wait", wait)
		}
		return nil
	})
	// A real reserve cannot take the cloud account. Inject evidence only on the
	// second request; the same daemon then reserves its qualifying EU route.
	f.agent.Scopes = append(f.agent.Scopes, "account.route")
	body := fmt.Sprintf(`{"run_id":%q,"daemon_id":"daemon-test","account_ids":[%q],"estimated_units":{"cost_micros":100}}`, run.ID, cloud)
	response := f.request(f.agent, "POST", "/api/agent-accounts/route", body, "")
	if response.Code != 409 {
		t.Fatal("cloud reserve escaped", response.Code, response.Body.String())
	}
	euAccount := f.queueAccount(t, 1000000)
	req := httptest.NewRequest("POST", "/api/agent-accounts/route", strings.NewReader(fmt.Sprintf(`{"run_id":%q,"daemon_id":"daemon-test","account_ids":[%q,%q],"estimated_units":{"cost_micros":100}}`, run.ID, cloud, euAccount)))
	req = req.WithContext(agentaccounts.WithResidencyClassifier(tenant.WithPrincipal(req.Context(), f.agent), prefsEvidence{euAccount}))
	req.Header.Set(agentruns.DaemonHeader, "daemon-test")
	req.Header.Set(agentruns.GenerationHeader, "generation-1")
	response = httptest.NewRecorder()
	f.mux.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var route struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	if route.AccountID != euAccount {
		t.Fatal("reserved outside EU", route)
	}
	// Keep a second queued alias run and starting/running turns on cloud accounts;
	// terminal and another starter's runs must not be touched by a You write.
	var queued agentruns.Run
	f.call(t, alias, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 201, &queued)
	ids := map[string]string{}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var otherPerson string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other starter') RETURNING id::text`, f.person.TenantID).Scan(&otherPerson); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,model_profile_id,prefs_person_id) VALUES($1,$2,$3,'queued',$4,$5) RETURNING id::text`, f.person.TenantID, o.NodeID, f.agent.ID, f.profile, otherPerson).Scan(&otherPerson); err != nil {
			return err
		}
		ids["other"] = otherPerson
		for _, status := range []string{"starting", "running", "waiting", "completed"} {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,model_profile_id,account_id,prefs_person_id,residency) VALUES($1,$2,$3,$4,$5,$6,$7,'eu') RETURNING id::text`, f.person.TenantID, o.NodeID, f.agent.ID, status, f.profile, cloud, f.person.ID).Scan(&id); err != nil {
				return err
			}
			ids[status] = id
		}
		return nil
	})
	var written struct {
		Revision       int64    `json:"revision"`
		RunningOutside []string `json:"running_outside"`
	}
	f.prefsCall(t, f.person, map[string]any{"revision": 1, "residency": "local"}, 200, &written)
	if len(written.RunningOutside) != 2 || !containsRun(written.RunningOutside, ids["starting"]) || !containsRun(written.RunningOutside, ids["running"]) {
		t.Fatal("running_outside mismatch", written)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, id := range []string{run.ID, queued.ID, ids["starting"], ids["running"], ids["waiting"]} {
			var stamp string
			if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, id).Scan(&stamp); err != nil {
				return err
			}
			if stamp != "local" {
				t.Fatal("missed non-terminal run", id, stamp)
			}
		}
		var stamp string
		if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, ids["completed"]).Scan(&stamp); err != nil {
			return err
		}
		if stamp != "eu" {
			t.Fatal("terminal run restamped")
		}
		var untouched *string
		if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, ids["other"]).Scan(&untouched); err != nil {
			return err
		}
		if untouched != nil {
			t.Fatal("another starter's run restamped")
		}
		return nil
	})
	count := f.count(t, f.person, `SELECT count(*) FROM events WHERE type='model.preferences_changed' AND jsonb_array_length(coalesce(after->'restamped','[]'::jsonb))>0`)
	// Simulate a pre-link stale stamp. A local-to-EU loosening must not catch it
	// up to EU; dispatch still reads the live requirement independently.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET residency=NULL WHERE id=$1`, queued.ID)
		return err
	})
	f.prefsCall(t, alias, map[string]any{"revision": 2, "residency": "eu"}, 200, &written)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var stamp *string
		if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, queued.ID).Scan(&stamp); err != nil {
			return err
		}
		if stamp != nil {
			t.Fatal("loosening caught up a stale stamp")
		}
		return nil
	})
	f.prefsCall(t, alias, map[string]any{"revision": 3, "residency": "any"}, 200, &written)
	if len(written.RunningOutside) != 0 || f.count(t, f.person, `SELECT count(*) FROM events WHERE type='model.preferences_changed' AND jsonb_array_length(coalesce(after->'restamped','[]'::jsonb))>0`) != count {
		t.Fatal("loosening restamped runs")
	}
	// The alias-scope guard remains authoritative for non-HTTP store clients.
	err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO model_pref_scopes(tenant_id,level,person_id) VALUES($1,'person',$2) RETURNING id`, alias.TenantID, alias.ID).Scan(new(string))
	if err == nil {
		t.Fatal("alias scope inserted")
	}
}
func containsRun(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
