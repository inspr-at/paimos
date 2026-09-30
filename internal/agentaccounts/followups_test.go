// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestQuotaHintsNeedPersonConfirmationForExactAccounts(t *testing.T) {
	reset(t)
	admin, runner, profile, _, mod := groupFixture(t)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	doors := []Account{
		groupAccount(t, mod, admin, runner, token, "a", "daemon-a", "First", "studio"),
		groupAccount(t, mod, admin, runner, token, "b", "daemon-b", "Second", "laptop"),
		groupAccount(t, mod, admin, runner, token, "c", "daemon-c", "Later", "laptop"),
	}
	fp := strings.Repeat("ab", 32)
	for _, a := range doors {
		callStatus(t, mod, &runner, token, "PUT", "/api/agent-accounts/"+a.ID+"/signals", encoded(t, accountSignals{"none", fp}), 204, nil)
	}
	count := func(account string, want int) {
		t.Helper()
		seed(t, admin, func(tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM (`+quotaAccounts+`) q`, account).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Fatalf("quota members=%d; want %d", n, want)
			}
			return nil
		})
	}
	count(doors[0].ID, 1)
	var plan []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &plan)
	for _, p := range plan {
		if p.SameQuotaAs != "" || p.Routing != nil && p.Routing.SameQuotaAs != "" {
			t.Fatal("hint collapsed gauges before confirmation")
		}
	}
	body := encoded(t, map[string]any{"account_ids": []string{doors[0].ID, doors[1].ID}, "quota_fingerprint": fp, "confirmed": true})
	callStatus(t, mod, &runner, token, "PUT", "/api/agent-accounts/quota-pool", body, 403, nil)
	viewer := addPrincipal(t, admin.TenantID, "person", "viewer", []string{"viewer"})
	callStatus(t, mod, &viewer, "", "PUT", "/api/agent-accounts/quota-pool", body, 403, nil)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", body, 204, nil)
	count(doors[0].ID, 2)
	count(doors[2].ID, 1) // Matching later signals never acquire consent.
	// Even the SQL reservation guard rejects spending a sibling ledger on a hint.
	run := insertRun(t, admin, runner, profile)
	seed(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, run, doors[2].ID)
		return err
	})
	err := db.InTenant(tenant.WithPrincipal(t.Context(), admin), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units) SELECT tenant_id,$1,id,1 FROM account_allowance_windows WHERE account_id=$2`, run, doors[0].ID)
		return err
	})
	if err == nil {
		t.Fatal("unconfirmed account joined a sibling ledger through SQL")
	}
	// Duplicate identities and a stale UI selection fail without changing consent.
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", encoded(t, map[string]any{"account_ids": []string{doors[0].ID, doors[0].ID}, "quota_fingerprint": fp, "confirmed": true}), 400, nil)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", strings.ReplaceAll(body, fp, strings.Repeat("cd", 32)), 409, nil)
	count(doors[0].ID, 2)
	foreign := makePrincipal(t, "foreign-pool", "person", "Foreign", []string{"admin"})
	callStatus(t, mod, &foreign, "", "PUT", "/api/agent-accounts/quota-pool", body, 404, nil)
	callStatus(t, mod, &runner, token, "PUT", "/api/agent-accounts/"+doors[0].ID+"/signals", encoded(t, accountSignals{"first_run", fp}), 204, nil)
	count(doors[0].ID, 2)
	callStatus(t, mod, &runner, token, "PUT", "/api/agent-accounts/"+doors[0].ID+"/signals", encoded(t, accountSignals{"first_run", strings.Repeat("cd", 32)}), 204, nil)
	count(doors[0].ID, 1)
	// Returning to the old signal cannot restore the person's prior confirmation.
	callStatus(t, mod, &runner, token, "PUT", "/api/agent-accounts/"+doors[0].ID+"/signals", encoded(t, accountSignals{"first_run", fp}), 204, nil)
	count(doors[0].ID, 1)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", body, 204, nil)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", encoded(t, map[string]any{"account_ids": []string{doors[1].ID}, "quota_fingerprint": fp, "confirmed": false}), 204, nil)
	count(doors[0].ID, 1)
	if scalar(t, admin, `SELECT count(*) FROM events WHERE type='account.quota_pool_confirmed' AND actor_principal_id=$1`, admin.ID) != 3 {
		t.Fatal("pool confirmation audit is missing")
	}
}

func TestPinWritesNeedTicketEditAndListsRespectVisibility(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	account := groupAccount(t, mod, admin, runner, token, "a", "daemon-a", "First", "studio")
	visible, ticket, _ := insertTicketRun(t, admin, runner, profile)
	hidden, hiddenTicket, _ := insertTreeRun(t, admin, runner, profile, "Private", true)
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{"harness": "codex", "name": "Work", "project_ids": []string{visible, hidden}}), 201, &group)
	pin := func(id string) string {
		return encoded(t, map[string]any{"ticket_id": id, "harness": "codex", "account_id": account.ID})
	}
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/pins", pin(ticket), 204, nil)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/pins", pin(hiddenTicket), 204, nil)
	reader := addPrincipal(t, admin.TenantID, "person", "reader", nil)
	var writerRole string
	seed(t, admin, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'routing_reader','Routing reader') RETURNING id::text`, admin.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'account.read'),($1,$2,'run.create')`, admin.TenantID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, admin.TenantID, reader.ID, role); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'ticket_editor','Ticket editor') RETURNING id::text`, admin.TenantID).Scan(&writerRole); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, admin.TenantID, writerRole); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, admin.TenantID, reader.ID, writerRole, visible)
		return err
	})
	var listed []AccountGroup
	callStatus(t, mod, &reader, "", "GET", "/api/agent-accounts/groups", "", 200, &listed)
	if len(listed) != 1 || len(listed[0].ProjectIDs) != 1 || listed[0].ProjectIDs[0] != visible {
		t.Fatalf("group leaked project IDs: %+v", listed)
	}
	var pins []TicketPin
	callStatus(t, mod, &reader, "", "GET", "/api/agent-accounts/pins?ticket_id="+hiddenTicket, "", 200, &pins)
	if len(pins) != 0 {
		t.Fatal("hidden ticket pin leaked")
	}
	callStatus(t, mod, &reader, "", "GET", "/api/agent-accounts/pins?ticket_id="+ticket, "", 200, &pins)
	if len(pins) != 1 {
		t.Fatal("visible pin missing")
	}
	callStatus(t, mod, &reader, "", "PUT", "/api/agent-accounts/pins", pin(hiddenTicket), 404, nil)
	callStatus(t, mod, &reader, "", "DELETE", "/api/agent-accounts/pins?ticket_id="+hiddenTicket+"&harness=codex", "", 404, nil)
	callStatus(t, mod, &reader, "", "PUT", "/api/agent-accounts/pins", pin(ticket), 403, nil)
	callStatus(t, mod, &reader, "", "DELETE", "/api/agent-accounts/pins?ticket_id="+ticket+"&harness=codex", "", 403, nil)
	seed(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.write')`, admin.TenantID, writerRole)
		return err
	})
	callStatus(t, mod, &reader, "", "PUT", "/api/agent-accounts/pins", pin(ticket), 204, nil)
	callStatus(t, mod, &reader, "", "DELETE", "/api/agent-accounts/pins?ticket_id="+ticket+"&harness=codex", "", 204, nil)
	if scalar(t, admin, `SELECT count(*) FROM account_ticket_pins WHERE ticket_id=$1`, hiddenTicket) != 1 {
		t.Fatal("hidden pin changed")
	}
}

func TestMovedQueuedRunReleasesBeforeWaiting(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	first := groupAccount(t, mod, admin, runner, token, "a", "daemon-a", "First", "studio")
	second := groupAccount(t, mod, admin, runner, token, "b", "daemon-a", "Second", "studio")
	project, run := insertProjectRun(t, admin, runner, profile, "Client")
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{"harness": "codex", "name": "Client", "account_ids": []string{first.ID}, "project_ids": []string{project}}), 201, &group)
	mustRoute(t, mod, runner, token, run, "daemon-a", []Account{first}, map[string]int64{"requests": 1})
	callStatus(t, mod, &admin, "", "PATCH", "/api/agent-accounts/groups/"+group.ID, encoded(t, map[string]any{"account_ids": []string{second.ID}}), 200, nil)
	seedUsed(t, admin, second.ID, 100)
	code, raw := call(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{first, second}, map[string]int64{"requests": 1}))
	var reply map[string]string
	_ = json.Unmarshal(raw, &reply)
	if code != 409 || reply["code"] != "account_moved_out_of_group" {
		t.Fatalf("moved run: %d %s", code, raw)
	}
	if scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run) != 0 || scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, run) != 0 {
		t.Fatal("waiting reroute kept obsolete holds")
	}
	seedUsed(t, admin, second.ID, 0)
	got := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{first, second}, map[string]int64{"requests": 1})
	if got.AccountID != second.ID {
		t.Fatal("reroute left current group")
	}
}

func TestGroupCascadeLookupIndexesExist(t *testing.T) {
	reset(t)
	admin, _, _, _, _ := groupFixture(t)
	for _, name := range []string{"account_run_targets_group", "account_ticket_pins_group", "account_ticket_pins_account"} {
		if scalar(t, admin, `SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname=$1`, name) != 1 {
			t.Fatalf("missing cascade lookup index %s", name)
		}
	}
}

// Revoking consent cannot strand accounting for an existing sibling ledger.
func TestChangedQuotaConfirmationReleasesOriginalLedgerAndReroutes(t *testing.T) {
	admin, runner, profile, token, mod, doors, now := sharedFixture(t)
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 99, ReadAt: now.Add(time.Second), ResetsAt: now.Add(time.Hour), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+doors[0].ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	run := insertRun(t, admin, runner, profile)
	mustRoute(t, mod, runner, token, run, doors[1].DaemonID, doors[1:], map[string]int64{"requests": 1})
	if scalar(t, admin, `SELECT count(*) FROM account_reservations r JOIN account_allowance_windows w ON w.id=r.window_id WHERE r.run_id=$1 AND w.account_id=$2`, run, doors[0].ID) != 1 {
		t.Fatal("fixture did not reserve a sibling ledger")
	}
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", encoded(t, map[string]any{"account_ids": []string{doors[1].ID}, "quota_fingerprint": strings.Repeat("ab", 32), "confirmed": false}), 204, nil)
	got := mustRoute(t, mod, runner, token, run, doors[1].DaemonID, doors[1:], map[string]int64{"requests": 1})
	if got.AccountID != doors[1].ID || scalar(t, admin, `SELECT count(*) FROM account_reservations r JOIN account_allowance_windows w ON w.id=r.window_id WHERE r.run_id=$1 AND r.state='active' AND w.account_id<>$2`, run, doors[1].ID) != 0 {
		t.Fatal("unconfirmed run retained a sibling hold")
	}
	seed(t, admin, func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, run, "", "") })
	if scalar(t, admin, `SELECT sum(reserved) FROM account_allowance_windows`) != 0 {
		t.Fatal("quota change stranded a hold")
	}
}
