// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Risk: duplicate harnesses either alias a default config home or silently lose
// enrollments/windows. Every isolated account must survive approval/redemption.
func TestComputerEnrollsTwoClaudeAndThreeCodexIsolatedAccounts(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	p.id = uuid(t, f.db)
	p.request["request_id"] = p.id
	p.device = nonce()
	p.request["device_hash"] = hash(p.device)
	accounts := []map[string]string{}
	for i, h := range []string{"claude", "claude", "codex", "codex", "codex"} {
		key := fmt.Sprintf("%s-%d", h, i)
		accounts = append(accounts, map[string]string{"account_key": key, "harness": h, "label": key, "model_profile_id": f.profiles[h], "config_home_id": hash("isolated-local-home-" + key)})
	}
	p.request["accounts"] = accounts
	f.submit(p)
	v := f.approve(p, "connect_only")
	if len(v.Enrollments) != 5 {
		t.Fatalf("got %d enrollments, want 2 Claude and 3 Codex", len(v.Enrollments))
	}
	v = f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	now := time.Now().UTC().Add(-time.Second)
	for i, e := range v.Enrollments {
		f.call("POST", "/api/agent-accounts/"+e.AccountID+"/capacity/approve", nil, true, "", 204)
		f.probe(v, e, key, 200)
		readings := []capacity.Reading{
			{WindowKind: "5h", WindowMinutes: 300, UsedPercent: float64(i + 10), ResetsAt: now.Add(time.Duration(i+1) * time.Hour), ReadAt: now, Source: "harness"},
			{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: float64(i + 20), ResetsAt: now.Add(time.Duration(i+24) * time.Hour), ReadAt: now, Source: "harness"},
		}
		f.call("POST", "/api/agent-accounts/"+e.AccountID+"/readings", map[string]any{"readings": readings}, false, key, 204)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=now() WHERE daemon_id=$1`, *v.DaemonID, f.person)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Account eligibility must not depend on the weekday or owner's office hours.
	schedule := capacity.DefaultSchedule()
	schedule.Reserve = capacity.ReserveOff
	for i := range schedule.Week {
		schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	for _, e := range v.Enrollments {
		f.call("PUT", "/api/agent-accounts/capacity/schedule", map[string]any{"scope": "account", "account_id": e.AccountID, "schedule": schedule}, true, "", 204)
	}
	for _, harness := range []string{"claude", "codex"} {
		var next agentaccounts.CapacityNext
		decodeResult(t, f.call("GET", "/api/agent-accounts/capacity/next?harness="+harness+"&daemon_id="+*v.DaemonID, nil, false, key, 200), &next)
		want := 2
		if harness == "codex" {
			want = 3
		}
		if len(next.Accounts) != want || next.ParallelRuns != want {
			t.Fatalf("lost eligible %s accounts: %+v", harness, next)
		}
	}
	var overview struct {
		Accounts []struct {
			AccountID string `json:"account_id"`
			Windows   []struct {
				Kind      string    `json:"window_kind"`
				Used      float64   `json:"used_percent"`
				Reset     time.Time `json:"resets_at"`
				Freshness string    `json:"freshness"`
			} `json:"windows"`
		} `json:"accounts"`
	}
	response := f.call("GET", "/api/agent-accounts/overview", nil, true, "", 200)
	decodeResult(t, response, &overview)
	if len(overview.Accounts) != 5 {
		t.Fatal("overview lost enrolled accounts")
	}
	for _, a := range overview.Accounts {
		if len(a.Windows) != 2 {
			t.Fatalf("account %s lost independent windows", a.AccountID)
		}
		for _, w := range a.Windows {
			if w.Freshness != "fresh" || w.Reset.IsZero() || w.Used < 10 {
				t.Fatalf("wrong window: %+v", w)
			}
		}
	}
	for _, forbidden := range []string{"account_key", "config_home_id", "isolated-local-home", "identity"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("overview leaked %s", forbidden)
		}
	}
	// Exercise the terminal vendor-stop reservation and claim through the real
	// auth/mux, retaining the immutable paired runtime identity.
	codex := []agentpairing.Enrollment{}
	for _, e := range v.Enrollments {
		if e.Harness == "codex" {
			codex = append(codex, e)
		}
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET state='unavailable' WHERE id=$1`, codex[2].AccountID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var next agentaccounts.CapacityNext
	decodeResult(t, f.call("GET", "/api/agent-accounts/capacity/next?harness=codex", nil, false, key, 200), &next)
	if len(next.Accounts) != 2 {
		t.Fatal("paired advice lost an available enrolled account")
	}
	var order workorders.Order
	decodeResult(t, f.call("POST", "/api/work-orders", map[string]any{"title": "Account handoff", "criteria": []string{"Continue on the spare"}, "assignee_principal_id": *v.PrincipalID}, true, "", 201), &order)
	decodeResult(t, f.call("PATCH", "/api/work-orders/"+order.NodeID, map[string]any{"expected_revision": order.Revision, "status": "ready"}, true, "", 200), &order)
	var run agentruns.Run
	decodeResult(t, f.call("POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": *v.PrincipalID, "model_profile_id": codex[0].ProfileID}, true, "", 201), &run)
	route := routeWithUnits(t, f, v, codex[0], key, run.ID, map[string]int{"requests": 1}, 200)
	f.call("POST", "/api/runs/"+run.ID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": reservationIDs(route)}, false, key, 200)
	stop := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 100, ResetsAt: now.Add(2 * time.Hour), ReadAt: now.Add(time.Second), Source: "harness", RunID: run.ID, Phase: "end"}
	f.call("POST", "/api/agent-accounts/"+codex[0].AccountID+"/readings", map[string]any{"readings": []capacity.Reading{stop}}, false, key, 204)
	r := f.request("POST", "/api/runs/"+run.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "failed", "error_code": "vendor_limit"}, false, key)
	r.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
	r.Header.Set(agentruns.GenerationHeader, "test-generation")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("vendor stop failed: %d %s", w.Code, w.Body.String())
	}
	var queued []agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, key, 200), &queued)
	if len(queued) != 1 || queued[0].RetryOfRunID == nil || *queued[0].RetryOfRunID != run.ID || queued[0].Wait != nil {
		t.Fatalf("terminal stop did not queue a routable handoff: %+v", queued)
	}
	route = routeWithUnits(t, f, v, codex[1], key, queued[0].ID, map[string]int{"requests": 1}, 200)
	if route.AccountID != codex[1].AccountID {
		t.Fatal("handoff did not reserve the spare account")
	}
	f.call("POST", "/api/runs/"+queued[0].ID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": reservationIDs(route)}, false, key, 200)
}

func TestPairingRejectsUnisolatedAndReusedHomesAcrossRequests(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	for _, homes := range [][]string{{"", ""}, {hash("separate"), ""}, {hash("same"), hash("same")}} {
		request := map[string]any{}
		for key, value := range p.request {
			request[key] = value
		}
		request["request_id"] = uuid(t, f.db)
		request["accounts"] = []map[string]string{
			{"account_key": "first", "harness": "claude", "label": "First", "config_home_id": homes[0]},
			{"account_key": "second", "harness": "claude", "label": "Second", "config_home_id": homes[1]},
		}
		w := f.call("POST", "/api/agent-pairing/device", request, false, "", 400)
		if !strings.Contains(w.Body.String(), "isolated config home") {
			t.Fatal("refusal did not explain config-home isolation")
		}
	}
	f.approve(p, "connect_only")
	v := f.redeem(p)
	additional := f.propose("claude")
	additional.id = uuid(t, f.db)
	additional.request["request_id"] = additional.id
	additional.device = nonce()
	additional.request["device_hash"] = hash(additional.device)
	additional.request["runtime_hash"] = hash(p.runtime)
	additional.request["lifecycle_hash"] = hash(p.lifecycle)
	additional.request["existing_computer_id"] = *v.ComputerID
	additional.request["existing_lifecycle_secret"] = p.lifecycle
	additional.request["accounts"] = []map[string]string{{"account_key": "claude-second", "harness": "claude", "label": "Second", "config_home_id": hash("separate")}}
	f.submit(additional)
	w := f.call("POST", "/api/agent-pairing/requests/"+additional.id+"/approve", map[string]any{"request_digest": additional.review.Digest, "verification": "connect_only", "selected_account_keys": []string{"claude-second"}}, true, "", 400)
	if !strings.Contains(w.Body.String(), "without isolation choose one account") {
		t.Fatal("Add harness bypassed the existing default-home enrollment")
	}
	var current agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &current)
	if len(current.Enrollments) != 1 {
		t.Fatal("refused approval left a partial enrollment")
	}
}
