// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/jackc/pgx/v5"
)

func TestManagedOrderActivationOnlyChangesFutureReviewBindings(t *testing.T) {
	f := newFixture(t)
	modelregistry.New(f.d.App).Mount(f.mux)
	strong := testID()
	f.tx(t, func(tx pgx.Tx) error {
		// Its own line: a catalog Opus route would otherwise run first.
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier,registered_effort_level) VALUES($1,$2,'saved-strong-review','1','claude','anthropic','claude-saved-review-1','xhigh','strong',4)`, f.person.TenantID, strong); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET max_parallel_runs=4 WHERE id=$1`, f.account)
		return err
	})
	var first Review
	input := f.input()
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", input, 201, &first)
	if first.RunID == nil || first.ProfileID == nil || *first.ProfileID != f.profile {
		t.Fatal("saved fixture review binding changed", first)
	}
	stored := func() string {
		var raw string
		f.tx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_build_object('review',to_jsonb(v),'run',to_jsonb(r))::text FROM work_order_reviews v JOIN agent_runs r ON r.work_order_id=v.work_order_id AND r.tenant_id=v.tenant_id WHERE v.work_order_id=$1`, first.OrderID).Scan(&raw)
		})
		return raw
	}
	original := stored()
	var ladder struct {
		Mode string `json:"order_mode"`
	}
	f.call(t, f.person, "GET", "/api/models/routes?role=review-gate", nil, 200, &ladder)
	if ladder.Mode != "saved" {
		t.Fatal("fixture has no saved Review order")
	}
	// The retired ladder editor's activation is now a Review order change.
	f.tx(t, func(tx pgx.Tx) error { return reviewOrderFirst(t, tx, strong) })
	if stored() != original {
		t.Fatal("activation rerouted or restamped queued review")
	}
	var replay Review
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", input, 201, &replay)
	if replay.RunID == nil || *replay.RunID != *first.RunID || !reflect.DeepEqual(replay.ProfileID, first.ProfileID) || stored() != original {
		t.Fatal("replay rebound existing review")
	}
	var next Review
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", f.input(), 201, &next)
	if next.RunID == nil || next.ProfileID == nil || *next.ProfileID != strong {
		t.Fatal("future review ignored saved order", next)
	}
	for _, tc := range []struct {
		review Review
		mode   string
	}{{first, "saved"}, {next, "saved"}} {
		var trace modelregistry.PreferenceTrace
		if err := json.Unmarshal(tc.review.Trace, &trace); err != nil {
			t.Fatal(err)
		}
		if trace.OrderMode != tc.mode {
			t.Fatal("creation trace lost order source", trace)
		}
		f.tx(t, func(tx pgx.Tx) error {
			var runTrace modelregistry.PreferenceTrace
			var account string
			if err := tx.QueryRow(t.Context(), `SELECT trace,COALESCE(account_id,requested_account_id)::text FROM agent_runs WHERE id=$1`, *tc.review.RunID).Scan(&runTrace, &account); err != nil {
				return err
			}
			if runTrace.OrderMode != tc.mode || account != f.account {
				t.Fatal("run did not capture order/account", runTrace, account)
			}
			return nil
		})
	}
	if stored() != original {
		t.Fatal("future dispatch modified prior bound work")
	}
}

func TestManagedSavedOrderFinishedTelemetryThroughProductionMiddleware(t *testing.T) {
	f := newFixture(t)
	strong := testID()
	f.tx(t, func(tx pgx.Tx) error {
		// Its own line: a catalog Opus route would otherwise run first.
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier,registered_effort_level) VALUES($1,$2,'completion-saved-review','1','claude','anthropic','claude-completion-review-1','xhigh','strong',4)`, f.person.TenantID, strong); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET max_parallel_runs=4 WHERE id=$1`, f.account)
		return err
	})
	source, report := f.completion(t)
	handler := f.boundaryHandler(t, f.d.App)
	cookie := f.boundaryCookie(t)
	read := httptest.NewRequest("GET", "/api/model-preferences/simple?for=default", nil)
	read.AddCookie(cookie)
	response := serveBoundary(handler, read)
	if response.Code != 200 {
		t.Fatal("snapshot middleware refusal", response.Code, response.Body.String())
	}
	// The retired ladder editor's activation is now a Review order change.
	f.tx(t, func(tx pgx.Tx) error { return reviewOrderFirst(t, tx, strong) })
	finished := serveBoundary(handler, boundaryRequest(t.Context(), "/api/runs/"+source+"/telemetry", report, f.token, nil))
	if finished.Code != 200 {
		t.Fatal("production completion failed", finished.Code, finished.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var reviewProfile, runProfile, account string
		var trace, runTrace modelregistry.PreferenceTrace
		if err := tx.QueryRow(t.Context(), `SELECT v.reviewer_profile_id::text,r.model_profile_id::text,COALESCE(r.account_id,r.requested_account_id)::text,v.trace,r.trace FROM work_order_reviews v JOIN agent_runs r ON r.tenant_id=v.tenant_id AND r.work_order_id=v.work_order_id WHERE v.author_run_id=$1`, source).Scan(&reviewProfile, &runProfile, &account, &trace, &runTrace); err != nil {
			return err
		}
		if reviewProfile != strong || runProfile != strong || account != f.account || trace.OrderMode != "saved" || runTrace.OrderMode != "saved" {
			t.Fatal("finished telemetry ignored saved dispatch or trace", trace, runTrace)
		}
		return nil
	})
	// Changing fallback after completion does not rebind an accepted replay.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM model_pref_situation_orders WHERE situation='review'`)
		return err
	})
	before := f.completionState(t)
	replay := serveBoundary(handler, boundaryRequest(t.Context(), "/api/runs/"+source+"/telemetry", report, f.token, nil))
	if replay.Code != 200 || f.completionState(t) != before {
		t.Fatal("mode change caused completion replay to settle/rebind", replay.Code, replay.Body.String())
	}
}

// reviewOrderFirst moves a profile's line to the front of the saved Review
// order, keeping its native effort and the rest of the fixture's order.
func reviewOrderFirst(t *testing.T, tx pgx.Tx, profile string) error {
	t.Helper()
	tag, err := tx.Exec(t.Context(), `UPDATE model_pref_situation_orders o SET rank=ARRAY[l.line]||array_remove(o.rank,l.line),efforts=coalesce(o.efforts,'{}')||jsonb_build_object(l.line,l.effort)
 FROM (SELECT aeon_models_line(family,harness,model) AS line,effort FROM model_profiles WHERE id=$1) l
 WHERE o.column_key='other' AND o.situation='review'`, profile)
	if err == nil && tag.RowsAffected() != 1 {
		err = fmt.Errorf("saved Review order missing")
	}
	return err
}
