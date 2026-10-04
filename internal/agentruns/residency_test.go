// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestTicketQueueStampsYouResidencyAndRejectsDisallowedAccount(t *testing.T) {
	f := setup(t)
	eu := "eu"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID, Residency: &eu})
		return err
	})
	account := f.queueAccount(t, 1000000)
	for _, targeted := range []bool{false, true} {
		ticket := f.ticket(t, "open", "high", nil)
		target := map[string]any{}
		if targeted {
			target = map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}
		}
		queued := f.addQueue(t, ticket, target)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var stamp string
			if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, queued.Run.ID).Scan(&stamp); err != nil {
				return err
			}
			if stamp != "eu" {
				t.Fatal("queue did not save the residency floor", stamp)
			}
			policy, err := modelprefs.RunRequirement(t.Context(), tx, queued.Run.ID)
			if err != nil {
				return err
			}
			if policy.PersonID == nil || *policy.PersonID != f.person.ID || policy.Residency != "eu" {
				t.Fatalf("queue lost starter's You residency: %+v", policy)
			}
			return nil
		})
	}
	ticket := f.ticket(t, "open", "high", nil)
	body, err := json.Marshal(map[string]any{"node_id": ticket, "agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account})
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(f.person, "POST", "/api/queue", string(body), "")
	if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"residency_unmet"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, ticket); n != 0 {
		t.Fatal("rejected account left a queued run")
	}
	local := "local"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID, Residency: &local})
		return err
	})
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE prefs_person_id=$1 AND queue_node_id IS NOT NULL AND residency='local'`, f.person.ID); n != 2 {
		t.Fatal("You tightening missed queued runs", n)
	}
}

func TestRunStoresLoosenedResidencyLockTrace(t *testing.T) {
	f := setup(t)
	f.queueAccount(t, 1000000)
	ticket := f.ticket(t, "open", "high", nil)
	project := uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'TRACE-1','Trace project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, ticket, project); err != nil {
			return err
		}
		eu, any := "eu", "any"
		if _, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID, Residency: &eu, ResidencyLocked: true}); err != nil {
			return err
		}
		_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "project", ProjectID: &project, Residency: &any})
		return err
	})
	queued := f.addQueue(t, ticket, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
	var ordinary agentruns.Run
	f.call(t, f.person, "POST", "/api/work-orders/"+queued.Run.OrderID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 201, &ordinary)
	for _, run := range []agentruns.Run{queued.Run, ordinary} {
		var loaded agentruns.Run
		f.call(t, f.person, "GET", "/api/runs/"+run.ID, nil, 200, &loaded)
		var trace modelprefs.RequirementTrace
		if err := json.Unmarshal(loaded.Trace, &trace); err != nil {
			t.Fatal(err)
		}
		if trace.PersonID == nil || *trace.PersonID != f.person.ID || trace.Residency.Value != "any" || !trace.Residency.LoosenedLock || len(trace.Residency.LoosenedLocks) != 1 || trace.Residency.LoosenedLocks[0] != (modelprefs.ResidencyLock{Level: "person", Value: "eu"}) {
			t.Fatalf("run lost loosening evidence: %+v", trace)
		}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var stamp *string
			if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, run.ID).Scan(&stamp); err != nil {
				return err
			}
			if stamp != nil {
				t.Fatal("looser chosen residency was not applied", *stamp)
			}
			return nil
		})
	}
}

func TestRunCreationStampsStarterAndRejectsDisallowedAccount(t *testing.T) {
	for _, kind := range []string{"person", "creator-key", "operator-key"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			o := f.order(t, nil)
			requester := f.person
			want := f.person.ID
			if strings.Contains(kind, "key") {
				requester = f.agent
				if kind == "creator-key" {
					requester.KeyCreatorID = f.person.ID
				} else {
					want = ""
				}
			}
			eu := "eu"
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "default", Residency: &eu})
				return err
			})
			var run agentruns.Run
			f.call(t, requester, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 201, &run)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var starter *string
				var residency string
				if err := tx.QueryRow(t.Context(), `SELECT prefs_person_id::text,residency FROM agent_runs WHERE id=$1`, run.ID).Scan(&starter, &residency); err != nil {
					return err
				}
				if residency != "eu" || (want == "" && starter != nil) || (want != "" && (starter == nil || *starter != want)) {
					t.Fatal("wrong run stamp", kind, residency, starter)
				}
				return nil
			})
			account := uuid()
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label)
   VALUES($1,$2,'residency-account','codex','daemon-test',$3,'Any provider')`, f.person.TenantID, account, f.agent.ID)
				return err
			})
			body := `{"agent_principal_id":"` + f.agent.ID + `","model_profile_id":"` + f.profile + `","requested_account_id":"` + account + `"}`
			response := f.request(f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", body, "")
			if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"residency_unmet"`) {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
}

func TestCanonicalWorkResidencyAdmissionAndLiveRecheck(t *testing.T) {
	f := setup(t)
	order := f.order(t, nil)
	leaf := bindOrderToWorkLeaf(t, f, order.NodeID)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"residency":"eu"}'::jsonb WHERE id=$1`, leaf)
		return err
	})
	account := f.queueAccount(t, 1000000)
	var rejected struct {
		Code string `json:"code"`
	}
	f.call(t, f.person, "POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account}, 409, &rejected)
	if rejected.Code != "residency_unmet" {
		t.Fatalf("wrong admission rejection: %+v", rejected)
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE work_order_id=$1`, order.NodeID) != 0 {
		t.Fatal("rejected admission left a run")
	}
	run := f.run(t, order)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var stamp string
		if err := tx.QueryRow(t.Context(), `SELECT residency FROM agent_runs WHERE id=$1`, run.ID).Scan(&stamp); err != nil {
			return err
		}
		if stamp != "eu" {
			t.Fatal("work residency lost during admission", stamp)
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"residency":"local"}'::jsonb WHERE id=$1`, leaf)
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		policy, err := modelprefs.RunRequirement(t.Context(), tx, run.ID)
		if err != nil {
			return err
		}
		if policy.Residency != "local" {
			t.Fatal("live work residency tightening ignored", policy.Residency)
		}
		return nil
	})
}
