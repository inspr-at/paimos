// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestManagedOrderActivationOnlyChangesFutureReviewBindings(t *testing.T) {
	f := newFixture(t)
	modelregistry.New(f.d.App).Mount(f.mux)
	strong := testID()
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'saved-strong-review','1','claude','anthropic','opus','xhigh','strong')`, f.person.TenantID, strong); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'review-gate',99,$2)`, f.person.TenantID, strong); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET max_parallel_runs=4 WHERE id=$1`, f.account)
		return err
	})
	var first Review
	input := f.input()
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", input, 201, &first)
	if first.RunID == nil || first.ProfileID == nil || *first.ProfileID != f.profile {
		t.Fatal("legacy review binding changed", first)
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
		Routes []modelregistry.Route `json:"routes"`
		Token  string                `json:"edit_token"`
		Mode   string                `json:"order_mode"`
	}
	f.call(t, f.person, "GET", "/api/models/routes?role=review-gate", nil, 200, &ladder)
	if ladder.Mode != "legacy" {
		t.Fatal("not opted out")
	}
	desired := []modelregistry.Route{}
	for _, row := range ladder.Routes {
		if row.ProfileID == strong {
			desired = append(desired, row)
		}
	}
	for _, row := range ladder.Routes {
		if row.ProfileID != strong {
			desired = append(desired, row)
		}
	}
	for i := range desired {
		desired[i].Priority = i + 1
	}
	raw, _ := json.Marshal(desired)
	req := httptest.NewRequest("PUT", "/api/models/routes?role=review-gate&order_mode=saved", strings.NewReader(string(raw)))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), f.person))
	req.Header.Set("If-Match", ladder.Token)
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, req)
	if response.Code != 200 || response.Header().Get("Model-Order-Mode") != "saved" {
		t.Fatal("activation failed", response.Code, response.Body.String())
	}
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
	}{{first, "legacy"}, {next, "saved"}} {
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
