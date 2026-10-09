// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/jackc/pgx/v5"
)

// Risk: a denied door supplies selection, model qualification, a pinned route
// or executable account hand-out. Each assertion keeps an allowed sibling.
func TestAccountContextSelectionPaths(t *testing.T) {
	reset(t)
	person, runner, profile, token, mod := groupFixture(t)
	denied := groupAccount(t, mod, person, runner, token, "denied", "daemon-a", "Denied", "test")
	allowed := groupAccount(t, mod, person, runner, token, "allowed", "daemon-a", "Allowed", "test")
	project, ticket, runID := insertTicketRun(t, person, runner, profile)
	seed(t, person, func(tx pgx.Tx) error {
		return putPin(t.Context(), tx, person, pinWrite{TicketID: ticket, Harness: "codex", AccountID: denied.ID})
	})
	seed(t, person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, denied.ID)
		return err
	})
	t.Run("1 dispatch narrowing and 16 existing ticket pin wait", func(t *testing.T) {
		seed(t, person, func(tx pgx.Tx) error {
			run, err := loadWaitRun(t.Context(), tx, runID)
			if err != nil {
				return err
			}
			kept, _, err := narrowCandidates(t.Context(), tx, run, "codex", []Account{denied, allowed})
			if err != nil {
				return err
			}
			if len(kept) != 0 {
				t.Fatal("denied pin spilled to sibling", kept)
			}
			wait, err := WaitForRun(t.Context(), tx, runID)
			if err != nil {
				return err
			}
			if wait == nil || wait.Code != "context" || wait.RunNowAllowed || wait.Until != nil {
				t.Fatal("denied pin has wrong wait", wait)
			}
			return nil
		})
	})
	t.Run("6 qualification 7 board counts and 8 review", func(t *testing.T) {
		seed(t, person, func(tx pgx.Tx) error {
			now := time.Now()
			ids, err := QualifyingAccountIDs(t.Context(), tx, profile, "codex", project, "any", now)
			if err != nil {
				return err
			}
			if !slices.Equal(ids, []string{allowed.ID}) {
				t.Fatal("qualification widened", ids)
			}
			counts, err := ResidencyProfileRouteCounts(t.Context(), tx, map[string]string{profile: "codex"}, project, "any", now)
			if err != nil {
				return err
			}
			if counts[profile] != 1 {
				t.Fatal("board counted denied door", counts)
			}
			accounts, err := ReviewAccounts(t.Context(), tx, profile, "codex", project, now)
			if err != nil {
				return err
			}
			if len(accounts) != 1 || accounts[0].ID != allowed.ID {
				t.Fatal("review widened", accounts)
			}
			account, err := ReviewAccount(t.Context(), tx, profile, "codex", project, now)
			if err != nil {
				return err
			}
			if account == nil || account.ID != allowed.ID {
				t.Fatal("review wrapper widened", account)
			}
			return nil
		})
	})

	t.Run("2 SQL prefilter and C0 dispatch", func(t *testing.T) {
		seed(t, person, func(tx pgx.Tx) error {
			// Remove only the old pin so it cannot be the refusal reason.
			if err := deletePin(t.Context(), tx, person, ticket, "codex"); err != nil {
				return err
			}
			run, err := loadWaitRun(t.Context(), tx, runID)
			if err != nil {
				return err
			}
			if _, _, _, err := selectAccount(t.Context(), tx, run, runner.ID, "codex", profile, "daemon-a", []string{denied.ID}, map[string]int64{"requests": 1}, time.Now()); err == nil {
				t.Fatal("denied SQL candidate reached admission")
			}
			selected, _, _, err := selectAccount(t.Context(), tx, run, runner.ID, "codex", profile, "daemon-a", []string{allowed.ID}, map[string]int64{"requests": 1}, time.Now())
			if err != nil {
				return err
			}
			if selected.ID != allowed.ID {
				t.Fatal(selected)
			}
			return nil
		})
	})
	t.Run("15 run target and 16 new ticket pin", func(t *testing.T) {
		seed(t, person, func(tx pgx.Tx) error {
			for _, err := range []error{setRunTarget(t.Context(), tx, person, runID, denied.ID, ""), putPin(t.Context(), tx, person, pinWrite{TicketID: ticket, Harness: "codex", AccountID: denied.ID})} {
				if err == nil || err.Error() != accountuse.NotAllowed {
					t.Fatalf("denied explicit target: %v", err)
				}
			}
			return nil
		})
	})
	t.Run("17 start catalog", func(t *testing.T) {
		var catalog Catalog
		callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/catalog?project_id="+project, nil, 200, &catalog)
		found := false
		for _, host := range catalog.Hosts {
			for _, h := range host.Harnesses {
				for _, a := range h.Accounts {
					if a.ID == denied.ID {
						found = true
						if a.Available || a.Wait == nil || a.Wait.Code != "context" || h.DefaultAccountID != nil && *h.DefaultAccountID == denied.ID {
							t.Fatal("denied catalog choice", a)
						}
					}
				}
			}
		}
		if !found {
			t.Fatal("denied choice vanished instead of explaining context")
		}
	})
	t.Run("18 capacity next and 19 use", func(t *testing.T) {
		var next CapacityNext
		callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/capacity/next?harness=codex&project_id="+project, nil, 200, &next)
		if len(next.Accounts) != 1 || next.Accounts[0].AccountID != allowed.ID {
			t.Fatal("capacity hand-out widened", next)
		}
		callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/use?harness=codex&account_id="+denied.ID+"&project_id="+project, nil, 409, nil)
		callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/use?harness=codex&account_id="+denied.ID, nil, 409, nil)
		var result UseResult
		callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/use?harness=codex&account_id="+allowed.ID+"&project_id="+project, nil, 200, &result)
		if len(result.Accounts) != 1 || result.Accounts[0].AccountID != allowed.ID {
			t.Fatal(result)
		}
	})
	t.Run("24 project health and 25 complete displays", func(t *testing.T) {
		seed(t, person, func(tx pgx.Tx) error {
			health, err := HarnessHealthAt(t.Context(), tx, time.Now(), project)
			if err != nil {
				return err
			}
			if health["codex"].Accounts != 2 || health["codex"].ContextDenied != 1 {
				t.Fatal(health)
			}
			accounts, err := listAccounts(t.Context(), tx)
			if err != nil {
				return err
			}
			if len(accounts) != 2 {
				t.Fatal("display totals narrowed", accounts)
			}
			for _, a := range accounts {
				if a.ID == allowed.ID && len(a.Contexts) == 0 {
					t.Fatal("missing context labels")
				}
			}
			return nil
		})
	})
	seed(t, person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, allowed.ID)
		return err
	})
	t.Run("all-denied next is context", func(t *testing.T) {
		var next CapacityNext
		callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/capacity/next?harness=codex&project_id="+project, nil, 200, &next)
		if len(next.Accounts) != 0 || next.Wait == nil || next.Wait.Code != "context" {
			t.Fatal(next)
		}
	})
}

// Risk: a denied account's headroom or unknown reading alters daily decisions;
// a filtered decision mutates the complete accounting projection (C1/C3).
func TestProjectDailySnapshotCapsContract(t *testing.T) {
	reset(t)
	person, runner, _, token, mod := groupFixture(t)
	allowed := groupAccount(t, mod, person, runner, token, "caps-allowed", "daemon-a", "Allowed", "test")
	denied := groupAccount(t, mod, person, runner, token, "caps-denied", "daemon-a", "Denied", "test")
	seed(t, person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, denied.ID)
		return err
	})
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	end := now.Add(12 * time.Hour)
	percent := func(v float64) *float64 { return &v }
	door := func(id string, used float64) agentplan.DailyAccount {
		return agentplan.DailyAccount{AccountID: id, UsedPct: percent(used), LimitUsedPct: percent(50), ReadAt: &now, ResetsAt: &end, Freshness: "fresh"}
	}
	for _, tc := range []struct {
		name            string
		allowed, denied agentplan.DailyAccount
		want            string
	}{
		{"a denied headroom", door(allowed.ID, 60), door(denied.ID, 10), "daily_limit"},
		{"b denied unknown", door(allowed.ID, 60), agentplan.DailyAccount{AccountID: denied.ID, Freshness: "stale"}, "daily_limit"},
		{"d allowed unknown", agentplan.DailyAccount{AccountID: allowed.ID, Freshness: "stale"}, door(denied.ID, 10), "daily_limit_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, person, func(tx pgx.Tx) error {
				d := agentplan.DefaultDaily()
				d.AtLimit = "ladder"
				s := agentplan.Snapshot{Plan: agentplan.Plan{Total: 5, Daily: map[string]agentplan.DailySettings{"codex": d}}, DailyUntil: end, DailyState: map[string]agentplan.DailyState{"codex": agentplan.SummarizeDaily([]agentplan.DailyAccount{tc.allowed, tc.denied})}}
				before, _ := json.Marshal(s)
				filtered, allDenied, err := ProjectDailySnapshot(t.Context(), tx, s, "")
				if err != nil {
					return err
				}
				decision := agentplan.DailyStart(filtered, "codex", now)
				if allDenied["codex"] || decision.Reason != tc.want || decision.FollowLadder != (tc.want == "daily_limit") {
					t.Fatal(decision, allDenied)
				}
				after, _ := json.Marshal(s)
				if string(before) != string(after) || len(s.DailyState["codex"].Accounts) != 2 {
					t.Fatal("f totals changed")
				}
				return nil
			})
		})
	}
	t.Run("c every owned account denied", func(t *testing.T) {
		seed(t, person, func(tx pgx.Tx) error {
			s := agentplan.Snapshot{DailyState: map[string]agentplan.DailyState{"codex": agentplan.SummarizeDaily([]agentplan.DailyAccount{door(denied.ID, 10)})}}
			filtered, allDenied, err := ProjectDailySnapshot(t.Context(), tx, s, "")
			if err != nil {
				return err
			}
			if !allDenied["codex"] || len(filtered.DailyState["codex"].Accounts) != 0 || len(s.DailyState["codex"].Accounts) != 1 {
				t.Fatal("all-denied conflated with unknown", allDenied)
			}
			return nil
		})
	})
}
