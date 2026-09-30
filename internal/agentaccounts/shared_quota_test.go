// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReservedRunRechecksFence(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	first := groupAccount(t, mod, admin, runner, token, "first", "daemon-a", "First", "studio")
	second := groupAccount(t, mod, admin, runner, token, "second", "daemon-a", "Second", "studio")
	project, run := insertProjectRun(t, admin, runner, profile, "Client")
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{"harness": "codex", "name": "Client", "exclusive": true, "account_ids": []string{first.ID}, "project_ids": []string{project}}), 201, &group)
	mustRoute(t, mod, runner, token, run, "daemon-a", []Account{first}, map[string]int64{"requests": 1})
	callStatus(t, mod, &admin, "", "PATCH", "/api/agent-accounts/groups/"+group.ID, encoded(t, map[string]any{"account_ids": []string{second.ID}}), 200, nil)
	seed(t, admin, func(tx pgx.Tx) error {
		if err := ValidateReservedCapacity(t.Context(), tx, run, first.ID); err == nil {
			t.Error("claim validation accepted a reserved account that is now outside the project fence")
		}
		return nil
	})
	code, _ := call(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{first}, map[string]int64{"requests": 1}))
	if code != 409 {
		t.Errorf("route replay status=%d; want 409 after member removal", code)
	}
}
func TestSharedQuotaReservations(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	token = issueKey(t, runner, []string{"account.manage", "account.probe"})
	first := groupAccount(t, mod, admin, runner, token, "first", "daemon-a", "First", "studio")
	second := groupAccount(t, mod, admin, runner, token, "second", "daemon-b", "Second", "laptop")
	seed(t, admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$1,max_parallel_runs=4`, strings.Repeat("ab", 32)); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM account_allowance_windows`)
		return err
	})
	now := time.Now().UTC()
	for _, a := range []Account{first, second} {
		reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 99, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness"}
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
		s := capacity.DefaultSchedule("UTC")
		s.Override = "sprint"
		s.Reserve = capacity.ReserveOff
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: a.ID, Schedule: &s}), 204, nil)
	}
	run := insertRun(t, admin, runner, profile)
	mustRoute(t, mod, runner, token, run, "daemon-a", []Account{first}, map[string]int64{"requests": 1})
	other := insertRun(t, admin, runner, profile)
	code, _ := call(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, other, "daemon-b", []Account{second}, map[string]int64{"requests": 1}))
	if code != 409 {
		t.Errorf("second door reserved status=%d; quota has only 1%% left and first door already reserved it", code)
	}
}
func TestSharedQuotaVendorDenial(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	token = issueKey(t, runner, []string{"account.manage", "account.probe"})
	first := groupAccount(t, mod, admin, runner, token, "first", "daemon-a", "First", "studio")
	second := groupAccount(t, mod, admin, runner, token, "second", "daemon-b", "Second", "laptop")
	seed(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$1`, strings.Repeat("ab", 32))
		return err
	})
	now := time.Now().UTC()
	denied := false
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 100, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness", OrdinaryUsageAllowed: &denied}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+first.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	run := insertRun(t, admin, runner, profile)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{first}, map[string]int64{"requests": 1}), 409, nil)
	code, _ := call(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-b", []Account{second}, map[string]int64{"requests": 1}))
	if code != 409 {
		t.Errorf("same-login alternate door status=%d; want shared vendor denial", code)
	}
}

func TestMoveAndWritesStayFenced(t *testing.T) {
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	member := groupAccount(t, mod, admin, runner, token, "member", "daemon-a", "Member", "studio")
	outside := groupAccount(t, mod, admin, runner, token, "outside", "daemon-a", "Outside", "studio")
	project, _, run := insertTicketRun(t, admin, runner, profile)
	var group AccountGroup
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{"harness": "codex", "name": "Client", "exclusive": true, "account_ids": []string{member.ID}, "project_ids": []string{project}}), 201, &group)
	for _, item := range []struct{ method, path string }{
		{"POST", "/api/agent-accounts/groups"}, {"PATCH", "/api/agent-accounts/groups/" + group.ID}, {"DELETE", "/api/agent-accounts/groups/" + group.ID},
		{"PUT", "/api/agent-accounts/pins"}, {"DELETE", "/api/agent-accounts/pins"}, {"POST", "/api/agent-accounts/runs/" + run + "/target"},
	} {
		callStatus(t, mod, &runner, token, item.method, item.path, `{}`, 403, nil)
	}
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/runs/"+run+"/target", encoded(t, map[string]string{"account_id": outside.ID}), 204, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1}), 409, nil)
	foreign := addPrincipal(t, admin.TenantID, "agent", "foreign", nil)
	foreignToken := issueKey(t, foreign, []string{"account.manage"})
	foreignAccount := groupAccount(t, mod, admin, foreign, foreignToken, "foreign", "daemon-b", "Foreign", "laptop")
	_, foreignRun := insertProjectRun(t, admin, runner, profile, "Other")
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/runs/"+foreignRun+"/target", encoded(t, map[string]string{"account_id": foreignAccount.ID}), 204, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, foreignRun, "daemon-b", []Account{foreignAccount}, map[string]int64{"requests": 1}), 409, nil)
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/runs/"+run+"/target", encoded(t, map[string]string{"group_id": group.ID}), 204, nil)
	got := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{member, outside}, map[string]int64{"requests": 1})
	if got.AccountID != member.ID {
		t.Error("valid group move did not use group member")
	}
}

func sharedFixture(t *testing.T) (tenant.Principal, tenant.Principal, string, string, httpapi.Module, []Account, time.Time) {
	t.Helper()
	reset(t)
	admin, runner, profile, token, mod := groupFixture(t)
	token = issueKey(t, runner, []string{"account.manage", "account.probe"})
	doors := []Account{
		groupAccount(t, mod, admin, runner, token, "door-a", "daemon-a", "First", "studio"),
		groupAccount(t, mod, admin, runner, token, "door-b", "daemon-b", "Second", "laptop"),
	}
	seed(t, admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$1,max_parallel_runs=4`, strings.Repeat("ab", 32)); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM account_allowance_windows`)
		return err
	})
	now := time.Now().UTC().Add(-2 * time.Second).Truncate(time.Microsecond)
	for _, a := range doors {
		reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 99, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness"}
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
		s := capacity.DefaultSchedule("UTC")
		s.Override = "sprint"
		s.Reserve = capacity.ReserveOff
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: a.ID, Schedule: &s}), 204, nil)
	}
	return admin, runner, profile, token, mod, doors, now
}

func TestSharedQuotaConcurrentReservationReplayAndRelease(t *testing.T) {
	admin, runner, profile, token, mod, doors, now := sharedFixture(t)
	runs := []string{insertRun(t, admin, runner, profile), insertRun(t, admin, runner, profile)}
	bodies := []string{routeBody(t, runs[0], doors[0].DaemonID, doors[:1], map[string]int64{"requests": 1}), routeBody(t, runs[1], doors[1].DaemonID, doors[1:], map[string]int64{"requests": 1})}
	type result struct{ index, code int }
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := range doors {
		go func(i int) {
			<-start
			code, _ := call(t, mod, &runner, token, "POST", "/api/agent-accounts/route", bodies[i])
			results <- result{i, code}
		}(i)
	}
	close(start)
	winner := -1
	for range doors {
		result := <-results
		if result.code == http.StatusOK {
			if winner != -1 {
				t.Fatal("both doors spent the last percent")
			}
			winner = result.index
		} else if result.code != http.StatusConflict {
			t.Fatalf("route status %d", result.code)
		}
	}
	if winner < 0 {
		t.Fatal("neither door could reserve the remaining percent")
	}
	loser := 1 - winner
	if n := scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE state='active'`); n != 1 {
		t.Fatalf("shared ledger has %d holds", n)
	}
	// Replay resolves the local door even when its window belongs to its sibling.
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", bodies[winner], 200, nil)
	// A fresher snapshot on the other door must retain the existing reservation.
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 99, ReadAt: now.Add(time.Second), ResetsAt: now.Add(time.Hour), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+doors[loser].ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", bodies[loser], 409, nil)
	seed(t, admin, func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, runs[winner], "", "") })
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", bodies[loser], 200, nil)
	seed(t, admin, func(tx pgx.Tx) error { return ValidateReservedCapacity(t.Context(), tx, runs[loser], doors[loser].ID) })
	seed(t, admin, func(tx pgx.Tx) error { return Settle(t.Context(), tx, runner, runs[loser]) })
	if n := scalar(t, admin, `SELECT sum(reserved) FROM account_allowance_windows`); n != 0 {
		t.Fatalf("settlement left %d held", n)
	}
}

func TestSharedQuotaDenialBlocksReservedSiblingAtLaunch(t *testing.T) {
	admin, runner, profile, token, mod, doors, now := sharedFixture(t)
	run := insertRun(t, admin, runner, profile)
	mustRoute(t, mod, runner, token, run, doors[1].DaemonID, doors[1:], map[string]int64{"requests": 1})
	denied := false
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 100, ReadAt: now.Add(time.Second), ResetsAt: now.Add(time.Hour), Source: "harness", OrdinaryUsageAllowed: &denied}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+doors[0].ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	seed(t, admin, func(tx pgx.Tx) error {
		if err := ValidateReservedCapacity(t.Context(), tx, run, doors[1].ID); err == nil {
			t.Error("sibling denial did not block launch")
		}
		return nil
	})
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, doors[1].DaemonID, doors[1:], map[string]int64{"requests": 1}), 409, nil)
}

func TestUseAccountIDRetainsLabelAndOwnership(t *testing.T) {
	reset(t)
	admin, runner, _, token, mod := groupFixture(t)
	label := `Claude Max; $(false) "quoted" 'apostrophe'`
	a := groupAccount(t, mod, admin, runner, token, "safe-key", "daemon-a", label, "studio")
	var out UseResult
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/use?harness=codex&account_id="+a.ID, "", 200, &out)
	if len(out.Accounts) != 1 || out.Accounts[0].Label != label || out.Accounts[0].AccountID != a.ID {
		t.Fatalf("wrong resolution: %+v", out)
	}
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/use?harness=codex&account_id="+a.ID+"&label=Main", "", 400, nil)
	foreign := makePrincipal(t, "foreign-use-id", "person", "Other", []string{"admin"})
	callStatus(t, mod, &foreign, "", "GET", "/api/agent-accounts/use?harness=codex&account_id="+a.ID, "", 404, nil)
}

func TestSharedQuotaRetryUsesSiblingsResetWithoutAuthorityBit(t *testing.T) {
	admin, runner, profile, token, mod, doors, now := sharedFixture(t)
	run := insertRun(t, admin, runner, profile)
	mustRoute(t, mod, runner, token, run, doors[0].DaemonID, doors[:1], map[string]int64{"requests": 1})
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 100, ReadAt: now.Add(time.Second), ResetsAt: now.Add(3 * time.Hour), Source: "harness", RunID: run, Phase: "end"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+doors[0].ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	unrelated := insertRun(t, admin, runner, profile)
	seed(t, admin, func(tx pgx.Tx) error {
		stopped := now.Add(2 * time.Second)
		until, known, err := VendorRetryAt(t.Context(), tx, doors[1].ID, run, stopped)
		if err != nil {
			return err
		}
		if !known || !until.Equal(reading.ResetsAt) {
			t.Fatalf("sibling reset ignored: %v known=%v", until, known)
		}
		until, known, err = VendorRetryAt(t.Context(), tx, doors[1].ID, unrelated, stopped)
		if err != nil {
			return err
		}
		if known || !until.Equal(stopped.Add(vendorStopBackoff)) {
			t.Fatalf("another run's sibling reading replaced the bounded backoff: %v known=%v", until, known)
		}
		return nil
	})
}
