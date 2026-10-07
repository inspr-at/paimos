// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: duplicate harnesses either alias a default config home or silently lose
// enrollments/windows. Every isolated account must survive approval/redemption.
func TestComputerEnrollsTwoClaudeAndThreeCodexIsolatedAccounts(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	p.id = uuid(t, f.db)
	p.request["request_id"] = p.id
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
	now := time.Now().UTC().Add(-time.Second)
	for i, e := range v.Enrollments {
		f.call("POST", "/api/agent-accounts/"+e.AccountID+"/capacity/approve", nil, true, "", 204)
		f.probe(v, e, p.runtime, 200)
		readings := []capacity.Reading{
			{WindowKind: "5h", WindowMinutes: 300, UsedPercent: float64(i + 10), ResetsAt: now.Add(time.Duration(i+1) * time.Hour), ReadAt: now, Source: "harness"},
			{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: float64(i + 20), ResetsAt: now.Add(time.Duration(i+24) * time.Hour), ReadAt: now, Source: "harness"},
		}
		f.call("POST", "/api/agent-accounts/"+e.AccountID+"/readings", map[string]any{"readings": readings}, false, p.runtime, 204)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2 WHERE daemon_id=$1`, *v.DaemonID, f.person)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"claude", "codex"} {
		var next agentaccounts.CapacityNext
		decodeResult(t, f.call("GET", "/api/agent-accounts/capacity/next?harness="+harness+"&daemon_id="+*v.DaemonID, nil, false, p.runtime, 200), &next)
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
