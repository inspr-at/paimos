// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestGroupFenceHoldsAndExclusiveWaits(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	member := groupAccount(t, mod, admin, runner, token, "member", "daemon-a", "Member", "build-7")
	outside := groupAccount(t, mod, admin, runner, token, "outside", "daemon-a", "Outside", "studio")
	project, run := insertProjectRun(t, admin, runner, profile, "Client")
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{
		"harness": "codex", "name": "Client", "exclusive": false,
		"account_ids": []string{member.ID}, "project_ids": []string{project},
	}), 201, &group)
	seedUsed(t, admin, member.ID, 80)
	got := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1})
	if got.AccountID != member.ID {
		t.Fatal("fenced project left its group")
	}

	reset(t)
	admin, runner, profile, token, mod = groupFixture(t)
	member = groupAccount(t, mod, admin, runner, token, "member", "daemon-a", "Member", "build-7")
	outside = groupAccount(t, mod, admin, runner, token, "outside", "daemon-a", "Outside", "studio")
	project, run = insertProjectRun(t, admin, runner, profile, "Client")
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{
		"harness": "codex", "name": "Client", "exclusive": true,
		"account_ids": []string{member.ID}, "project_ids": []string{project},
	}), 201, nil)
	seedUsed(t, admin, member.ID, 100)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1}), 409, nil)
	if scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, run) != 0 || scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE run_id=$1`, run) != 0 {
		t.Fatal("exclusive group spilled instead of waiting")
	}
	_, other := insertProjectRun(t, admin, runner, profile, "Other")
	seedUsed(t, admin, outside.ID, 80)
	got = mustRoute(t, mod, runner, token, other, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1})
	if got.AccountID != outside.ID {
		t.Fatal("exclusive member was used outside its projects")
	}
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{
		"harness": "codex", "name": "Empty", "exclusive": true, "project_ids": []string{},
	}), 400, nil)
}

func TestSharedQuotaCountsOnce(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	first := groupAccount(t, mod, admin, runner, token, "door-a", "daemon-a", "Spare", "build-7")
	second := groupAccount(t, mod, admin, runner, token, "door-b", "daemon-b", "Spare", "studio")
	fp := strings.Repeat("ab", 32)
	seed(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$2,quota_pool_fingerprint=$2 WHERE id=$1 OR id=$3`, first.ID, fp, second.ID)
		return err
	})
	var plan []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &plan)
	slots := 0
	hosts := 0
	for _, item := range plan {
		if item.AccountID == first.ID || item.AccountID == second.ID {
			if item.Routing != nil {
				slots += item.Routing.AvailableSlots
			}
			hosts += len(item.Hosts)
		}
	}
	if slots != 1 || hosts < 2 {
		t.Fatalf("quota counted apart: slots=%d hosts=%d plan=%+v", slots, hosts, plan)
	}
	run := insertRun(t, admin, runner, profile)
	if got := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{first}, map[string]int64{"requests": 1}); got.AccountID != first.ID {
		t.Fatal(got)
	}
	other := insertRun(t, admin, runner, profile)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, other, "daemon-b", []Account{second}, map[string]int64{"requests": 1}), 409, nil)
	if scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, other) != 0 {
		t.Fatal("second door of the same quota started")
	}
}

func TestGroupScheduleSitsBetweenAccountAndPool(t *testing.T) {
	reset(t)
	admin, runner, _, token, mod := groupFixture(t)
	account := groupAccount(t, mod, admin, runner, token, "main", "daemon-a", "Main", "studio")
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{
		"harness": "codex", "name": "Client", "account_ids": []string{account.ID},
	}), 201, &group)
	accountSchedule := capacity.DefaultSchedule()
	groupSchedule := capacity.DefaultSchedule()
	groupSchedule.Reserve, groupSchedule.ReservePercent = capacity.ReserveFixed, 40
	poolSchedule := capacity.DefaultSchedule()
	poolSchedule.Reserve = capacity.ReserveOff
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: account.ID, Schedule: &accountSchedule}), 204, nil)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "group", GroupID: group.ID, Schedule: &groupSchedule}), 204, nil)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &poolSchedule}), 204, nil)
	var plan []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &plan)
	if len(plan) != 1 || plan[0].Schedule.Reserve != capacity.ReserveFixed || plan[0].Schedule.ReservePercent != 40 || plan[0].GroupID != group.ID {
		t.Fatalf("group schedule did not sit between account and pool: %+v", plan)
	}
}

func TestUseAccountPayloadHasNoPath(t *testing.T) {
	reset(t)
	admin, runner, _, token, mod := groupFixture(t)
	first := groupAccount(t, mod, admin, runner, token, "door-a", "daemon-a", "Spare", "build-7")
	second := groupAccount(t, mod, admin, runner, token, "door-b", "daemon-b", "Spare", "studio")
	fp := strings.Repeat("cd", 32)
	seed(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$2,quota_pool_fingerprint=$2 WHERE id=$1 OR id=$3`, first.ID, fp, second.ID)
		return err
	})
	var body []byte
	code, body := call(t, mod, &admin, "", "GET", "/api/agent-accounts/use?harness=codex&label=Spare", "")
	if code != 200 {
		t.Fatalf("use %d %s", code, body)
	}
	assertNoPath(t, string(body))
	var out UseResult
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/use?harness=codex&label=spare", "", 200, &out)
	if len(out.Accounts) != 2 || out.Accounts[0].QuotaFingerprint != fp || out.Accounts[0].Label != "Spare" {
		t.Fatalf("%+v", out)
	}
	for _, door := range out.Accounts {
		if door.AccountID == "" || strings.Contains(door.DaemonID, "/") || door.HostLabel == "" {
			t.Fatalf("%+v", door)
		}
	}
	other := groupAccount(t, mod, admin, runner, token, "other", "daemon-a", "Spare", "studio")
	code, body = call(t, mod, &admin, "", "GET", "/api/agent-accounts/use?harness=codex&label=Spare", "")
	if code != 409 || strings.Contains(strings.ToLower(string(body)), "/users") {
		t.Fatalf("distinct quotas %d %s", code, body)
	}
	_ = other
	code, body = call(t, mod, &admin, "", "GET", "/api/agent-accounts/use?harness=codex&label=/Users/markus", "")
	if code != 400 {
		t.Fatalf("path label %d %s", code, body)
	}
}

func TestTicketPinDoesNotCrossAFence(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	member := groupAccount(t, mod, admin, runner, token, "member", "daemon-a", "Member", "build-7")
	outside := groupAccount(t, mod, admin, runner, token, "outside", "daemon-a", "Outside", "studio")
	project, ticket, run := insertTicketRun(t, admin, runner, profile)
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{
		"harness": "codex", "name": "Client", "exclusive": true,
		"account_ids": []string{member.ID}, "project_ids": []string{project},
	}), 201, &group)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/pins", encoded(t, map[string]any{
		"ticket_id": ticket, "harness": "codex", "account_id": outside.ID,
	}), 204, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1}), 409, nil)
	if scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, run) != 0 {
		t.Fatal("ticket pin crossed the fence")
	}
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/pins", encoded(t, map[string]any{
		"ticket_id": ticket, "harness": "codex", "group_id": group.ID,
	}), 204, nil)
	seedUsed(t, admin, outside.ID, 0)
	got := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1})
	if got.AccountID != member.ID {
		t.Fatal("ticket group pin was ignored", got.AccountID)
	}
}

func TestMigratedWorkRetainsAndEditsAccountPins(t *testing.T) {
	for _, target := range []string{"account", "group"} {
		t.Run(target, func(t *testing.T) {
			reset(t)
			admin, runner, profile, token, mod := groupFixture(t)
			pinned := groupAccount(t, mod, admin, runner, token, "pinned", "daemon-a", "Pinned", "studio")
			other := groupAccount(t, mod, admin, runner, token, "other", "daemon-a", "Other", "studio")
			_, ticket, run := insertTicketRun(t, admin, runner, profile)
			var group AccountGroup
			callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{"harness": "codex", "name": "Pinned group", "account_ids": []string{pinned.ID}}), 201, &group)
			pin := map[string]any{"ticket_id": ticket, "harness": "codex"}
			if target == "account" {
				pin["account_id"] = pinned.ID
			} else {
				pin["group_id"] = group.ID
			}
			// Seed a legacy pin through the real guard, then restore canonical work.
			// The full pre-1215 upgrade is covered by the database migration test.
			seed(t, admin, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET slug='ticket' WHERE slug='work'`); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO account_ticket_pins(tenant_id,ticket_id,harness,account_id,group_id) VALUES($1,$2,'codex',$3,$4)`, admin.TenantID, ticket, pin["account_id"], pin["group_id"]); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET slug='work' WHERE slug='ticket'`)
				return err
			})
			seedUsed(t, admin, pinned.ID, 80)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{other}, map[string]int64{"requests": 1}), 409, nil)
			if scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, run) != 0 {
				t.Fatal("retained pin allowed an unpinned account")
			}
			got := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{other, pinned}, map[string]int64{"requests": 1})
			if got.AccountID != pinned.ID {
				t.Fatal("retained work pin ignored", got.AccountID)
			}
			callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/pins", encoded(t, map[string]any{"ticket_id": ticket, "harness": "codex", "group_id": group.ID}), 204, nil)
			callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/pins", encoded(t, map[string]any{"ticket_id": ticket, "harness": "codex", "account_id": other.ID}), 204, nil)
			var pins []TicketPin
			callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/pins?ticket_id="+ticket, "", 200, &pins)
			if len(pins) != 1 || pins[0].AccountID != other.ID || pins[0].GroupID != "" {
				t.Fatalf("edited work pin not saved: %+v", pins)
			}
			callStatus(t, mod, &admin, "", "DELETE", "/api/agent-accounts/pins?ticket_id="+ticket+"&harness=codex", "", 204, nil)
			if scalar(t, admin, `SELECT count(*) FROM account_ticket_pins WHERE ticket_id=$1`, ticket) != 0 {
				t.Fatal("work pin deletion failed")
			}
		})
	}
}

func groupFixture(t *testing.T) (tenant.Principal, tenant.Principal, string, string, httpapi.Module) {
	t.Helper()
	admin := makePrincipal(t, "groups", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	return admin, runner, codexProfile(t, admin), issueKey(t, runner, []string{"account.manage"}), accountsMod()
}

func groupAccount(t *testing.T, mod httpapi.Module, admin, runner tenant.Principal, token, key, daemon, label, host string) Account {
	t.Helper()
	var account Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{
		"account_key": key, "harness": "codex", "daemon_id": daemon, "label": label,
	}), 201, &account)
	ownFixtureAccount(t, admin, &account)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+account.ID+"/probe", encoded(t, map[string]any{
		"daemon_id": daemon, "daemon_generation": "g1", "available": true, "host_label": host,
	}), 200, &account)
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/"+account.ID+"/windows", windowBody(time.Now().Add(-time.Minute), time.Now().Add(time.Hour), "requests", 100, "unrestricted"), 201, nil)
	fixtureAlwaysOn(t, mod, admin, account.ID)
	return account
}

// Routing-fence fixtures have real manual caps but no vendor measurements.
// Give them explicit hours so the asserted fence never depends on wall time.
func fixtureAlwaysOn(t *testing.T, mod httpapi.Module, person tenant.Principal, accountID string) {
	t.Helper()
	s := capacity.DefaultSchedule("UTC")
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	s.Reserve = capacity.ReserveOff
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: accountID, Schedule: &s}), 204, nil)
}

func insertProjectRun(t *testing.T, person, agent tenant.Principal, profileID, title string) (string, string) {
	t.Helper()
	project, _, run := insertTreeRun(t, person, agent, profileID, title, false)
	return project, run
}

func insertTicketRun(t *testing.T, person, agent tenant.Principal, profileID string) (string, string, string) {
	t.Helper()
	return insertTreeRun(t, person, agent, profileID, "Client", true)
}

func insertTreeRun(t *testing.T, person, agent tenant.Principal, profileID, title string, withTicket bool) (string, string, string) {
	t.Helper()
	var project, ticket, run string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title)
			SELECT $1::uuid, aeon_next_node_key($1::uuid, k.short_prefix), k.id, $2
			FROM node_kinds k WHERE k.slug = 'project'
			RETURNING id::text`, person.TenantID, title).Scan(&project); err != nil {
			return err
		}
		parent := project
		if withTicket {
			if err := tx.QueryRow(t.Context(), `
				INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id)
				SELECT $1::uuid, aeon_next_node_key($1::uuid, k.short_prefix), k.id, 'Ticket', $2::uuid
				FROM node_kinds k WHERE k.slug = 'work'
				RETURNING id::text`, person.TenantID, project).Scan(&ticket); err != nil {
				return err
			}
			parent = ticket
		}
		var nodeID string
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id)
			SELECT $1::uuid, aeon_next_node_key($1::uuid, k.short_prefix), k.id, 'Work', $2::uuid
			FROM node_kinds k WHERE k.slug = 'work_order'
			RETURNING id::text`, person.TenantID, parent).Scan(&nodeID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO work_orders (tenant_id, node_id, requested_by_principal_id, assignee_principal_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'ready')`,
			person.TenantID, nodeID, person.ID, agent.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `
			INSERT INTO agent_runs (tenant_id, work_order_id, agent_principal_id, model_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'queued')
			RETURNING id::text`, person.TenantID, nodeID, agent.ID, profileID).Scan(&run)
	})
	if err != nil {
		t.Fatalf("tree run: %v", err)
	}
	return project, ticket, run
}

func seed(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func seedUsed(t *testing.T, p tenant.Principal, accountID string, used int64) {
	t.Helper()
	seed(t, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET used=$2 WHERE account_id=$1::uuid`, accountID, used)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("no window for %s", accountID)
		}
		return nil
	})
}

func assertNoPath(t *testing.T, raw string) {
	t.Helper()
	lower := strings.ToLower(raw)
	for _, bad := range []string{"/users", "codex_home", "claude_config", "socket", `\`, "c:/"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("network payload contains %q", bad)
		}
	}
}
