// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workorders"
)

func TestMislabelledProfileRejectedAndLegacyRouteRetired(t *testing.T) {
	f := newFixture(t)
	modelregistry.New(f.d.App).Mount(f.mux)
	f.call(t, f.person, "POST", "/api/models", map[string]string{
		"slug": "mislabelled-grok", "version": "1", "harness": "grok", "model": "grok-4.7", "family": "anthropic", "effort": "xhigh", "tier": "frontier",
	}, 400, nil)
	id := testID()
	f.tx(t, func(tx pgx.Tx) error {
		// Simulate immutable history written by the old validator. The account
		// is eligible for this harness, so identity must be the rejecting gate.
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'legacy-mislabel','1','claude','xai','review-model','xhigh','frontier')`, f.person.TenantID, id); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state) VALUES($1,'review-gate',10,$2,'available')`, f.person.TenantID, id)
		return err
	})
	var resolved modelregistry.Resolution
	f.call(t, f.person, "GET", "/api/models/resolve?role=review-gate&author_family=anthropic&harness=claude", nil, 200, &resolved)
	if resolved.Profile != nil || !resolved.OwnerRequired {
		t.Fatal("legacy family spoof passed role resolution")
	}
	in := f.input()
	in.AuthorFamily = "anthropic"
	var v Review
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 201, &v)
	if v.RunID != nil || v.GateOpen {
		t.Fatal("legacy family spoof dispatched a same-family review")
	}
	found := false
	for _, candidate := range v.Ladder {
		if candidate.ProfileID == id {
			found = !candidate.Selected && strings.Contains(strings.Join(candidate.SkipReasons, ";"), "family does not match")
		}
	}
	if !found {
		t.Fatal("retired pin missing its identity rejection reason")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var family string
		if err := tx.QueryRow(t.Context(), `SELECT family FROM model_profiles WHERE id=$1`, id).Scan(&family); err != nil {
			return err
		}
		if family != "xai" {
			t.Fatal("immutable history was rewritten")
		}
		return nil
	})
}

func TestLegacyMislabelCannotRecordEvidenceOrKeepGreen(t *testing.T) {
	f := newFixture(t)
	p := &recordingPublisher{}
	f.m.publisher = p
	profile, run := testID(), testID()
	var order workorders.Order
	f.call(t, f.person, "POST", "/api/work-orders", workorders.CreateInput{Title: "Legacy review", Parent: &f.ticket, Assignee: &f.agent.ID}, 201, &order)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'legacy-mislabel','1','claude','xai','review-model','xhigh','frontier')`, f.person.TenantID, profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_orders SET kind='review',status='running' WHERE node_id=$1`, order.NodeID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,model_profile_id,account_id,daemon_id,daemon_generation,status,effective_model,model_evidence)
            VALUES($1,$2,$3,$4,$5,$6,'review-daemon','review-generation','running','review-model','vendor_reported')`, f.person.TenantID, run, order.NodeID, f.agent.ID, profile, f.account); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,ticket_snapshot,repository,base_sha,head_sha,author_family,reviewer_profile_id,reviewer_family,run_id,pull_request,github_status,github_reported_state)
            VALUES($1,$2,$3,$4,'{}','fixture','example/review-fixture',$5,$6,'anthropic',$7,'xai',$8,12,'success','success')`, f.person.TenantID, order.NodeID, f.ticket, testID(), strings.Repeat("a", 40), strings.Repeat("b", 40), profile, run)
		return err
	})
	f.call(t, f.agent, "POST", "/api/work-orders/"+order.NodeID+"/evidence", map[string]any{"kind": "text", "reference": "VERDICT: ok", "run_id": run}, 403, nil)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed' WHERE id=$1`, run); err != nil {
			return err
		}
		raw, _ := json.Marshal(reviewgate.Parse("VERDICT: ok"))
		_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET result=$2 WHERE work_order_id=$1`, order.NodeID, raw)
		return err
	})
	var rows []Review
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/reviews", nil, 200, &rows)
	if len(rows) != 1 || rows[0].GateOpen || statusState(rows[0]) != "error" {
		t.Fatal("legacy same-provider verdict retained approval")
	}
	f.m.reportStatuses(t.Context())
	if len(p.orders) != 1 || p.orders[0] != order.NodeID {
		t.Fatal("legacy green status was not revoked")
	}
}
