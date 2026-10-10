// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Risks 3/13/14: an explicit denied account can be queued or created, or a
// previously valid reservation starts after its matrix permission changes.
func TestRunAndQueueAccountContextChecks(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	run := f.run(t, o)
	ids := f.reserve(t, run)
	var account string
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT account_id::text FROM agent_runs WHERE id=$1`, run.ID).Scan(&account); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, account)
		return err
	})
	target := map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account}
	w := f.request(f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", marshalContextTest(t, target), "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "account_not_allowed_for_context") {
		t.Fatal("run create wrong refusal", w.Code, w.Body.String())
	}
	ticket := f.ticket(t, "open", "high", map[string]any{"estimate_hours": 1, "acceptance_criteria": "prove context"})
	target["node_id"] = ticket
	w = f.request(f.person, "POST", "/api/queue", marshalContextTest(t, target), "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "account_not_allowed_for_context") {
		t.Fatal("queue create wrong refusal", w.Code, w.Body.String())
	}
	t.Run("9 explicit independent review", func(t *testing.T) {
		review := o
		family := "openai"
		review.Kind = "review"
		review.Review = &reviewgate.Binding{ProfileID: &f.profile, ReviewerFamily: &family, AuthorFamily: "anthropic"}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := agentruns.QueueReview(t.Context(), tx, f.person, review, f.agent.ID, f.profile, account, &f.person.ID, "any", json.RawMessage(`{}`))
			if err == nil || err.Error() != "account_not_allowed_for_context" {
				t.Fatalf("explicit review wrong refusal: %v", err)
			}
			return nil
		})
	})
	claimFailure(t, f, run.ID, ids, "account_not_allowed_for_context")
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		var held int
		var status string
		if err := tx.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active') FROM agent_runs WHERE id=$1`, run.ID).Scan(&status, &held); err != nil {
			return err
		}
		if status != "queued" || held != 0 {
			t.Fatal("denied claim kept reservations or started", status, held)
		}
		return nil
	})
}

// Risks 11/12: queue discovery routes to an agent whose only account is denied;
// pickup spills past that denial. Keep a ready queued leaf in the fixture.
func TestQueueAccountContextDiscoveryAndPickup(t *testing.T) {
	f := setup(t)
	account := f.queueAccount(t, 1000000)
	ticket := f.ticket(t, "open", "high", map[string]any{"estimate_hours": 1, "acceptance_criteria": "prove context"})
	entry := f.addQueue(t, ticket, nil)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, account)
		return err
	})
	t.Run("24 project queue capacity excludes denied accounts", func(t *testing.T) {
		var project string
		f.tx(t, f.person, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Capacity project' FROM node_kinds k WHERE k.slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project)
		})
		var display, contextual qPage
		f.call(t, f.person, "GET", "/api/queue", nil, 200, &display)
		f.call(t, f.person, "GET", "/api/queue?project_id="+project, nil, 200, &contextual)
		if display.Capacity.Parallel != 1 || contextual.Capacity.Parallel != 0 {
			t.Fatal("queue advisory lost display totals or counted denied door", display.Capacity, contextual.Capacity)
		}
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		targets, _, err := workqueue.RouteCandidatesTx(t.Context(), tx, entry.Run.ID, []byte(`{"route_role":"build"}`), "", false, workqueue.RouteTarget{Profile: f.profile})
		if err != nil {
			return err
		}
		if len(targets) != 0 {
			t.Fatal("discovery counted denied agent", targets)
		}
		return nil
	})
	var picked struct{ Entry *qEntry }
	f.call(t, f.person, "POST", "/api/queue/next", map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 200, &picked)
	if picked.Entry != nil {
		t.Fatal("pickup spilled past context", picked)
	}
	var current agentruns.Run
	f.call(t, f.person, "GET", "/api/runs/"+entry.Run.ID, nil, 200, &current)
	if current.Status != "queued" || current.AccountID != nil {
		t.Fatal(current)
	}
}

func marshalContextTest(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
