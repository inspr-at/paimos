// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func hiddenWrite(t *testing.T, r *httptest.ResponseRecorder, status int) HiddenResult {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status %d want %d: %s", r.Code, status, r.Body)
	}
	var out HiddenResult
	if status == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func listIDs(list GoalList) []string {
	ids := make([]string, len(list.Items))
	for i, g := range list.Items {
		ids[i] = g.ID
	}
	return ids
}

func hiddenOf(list GoalList, id string) (found, hidden bool) {
	for _, g := range list.Items {
		if g.ID == id {
			return true, g.Hidden
		}
	}
	return false, false
}

// hiddenRows counts the hidden_goals rows a principal can read under RLS.
func hiddenRows(t *testing.T, f storageFixture, p tenant.Principal) int {
	t.Helper()
	var n int
	ctx := tenant.WithPrincipal(t.Context(), p)
	err := db.InTenant(ctx, f.db.App, p.TenantID, func(tx pgx.Tx) error {
		if err := db.EnterPrincipal(ctx, tx, p.TenantID, p.ID, nil, nil); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM hidden_goals`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Risk: hiding changes someone else's view, leaks across people or tenants, can
// be done by an agent or API key, double-writes on retry, drifts the state
// chips, miscounts hidden_count, or Undo puts the goal somewhere else.
func TestGoalHideForMePerPersonTenantAndKeys(t *testing.T) {
	f := writerFixture(t)
	m := &module{f.db.App, func() time.Time { return apiClock }}
	list := func(p tenant.Principal, query string) GoalList {
		return goalList(t, goalRequest(t.Context(), m, p, "GET", "/api/goals"+query, "", ""))
	}
	hide := "/api/me/goals/GOAL-2/hidden"

	before := list(f.writer, "")
	if found, _ := hiddenOf(before, f.secondGoal); !found || before.HiddenCount == nil || *before.HiddenCount != 0 {
		t.Fatalf("fixture: GOAL-2 visible and nothing hidden %v %v", listIDs(before), before.HiddenCount)
	}

	// Person A hides G; repeating it changes nothing.
	first := hiddenWrite(t, goalRequest(t.Context(), m, f.writer, "PUT", hide, "", ""), 200)
	if !first.Hidden || !first.Changed || first.GoalID != f.secondGoal || first.Key != "GOAL-2" {
		t.Fatalf("hide %+v", first)
	}
	if again := hiddenWrite(t, goalRequest(t.Context(), m, f.writer, "PUT", "/api/me/goals/"+f.secondGoal+"/hidden", "", ""), 200); !again.Hidden || again.Changed {
		t.Fatalf("hiding twice must be a no-op %+v", again)
	}

	// A's list leaves G out and counts it; the state chips keep counting it (one rule with Hide done).
	after := list(f.writer, "")
	if found, _ := hiddenOf(after, f.secondGoal); found || after.HiddenCount == nil || *after.HiddenCount != 1 {
		t.Fatalf("A still sees G or miscounts %v %v", listIDs(after), after.HiddenCount)
	}
	if after.Totals != before.Totals {
		t.Fatalf("hiding moved the totals %+v → %+v", before.Totals, after.Totals)
	}
	included := list(f.writer, "?hidden=include")
	if found, hidden := hiddenOf(included, f.secondGoal); !found || !hidden || *included.HiddenCount != 1 {
		t.Fatalf("hidden=include must return G marked hidden %v", listIDs(included))
	}
	if !slices.Equal(listIDs(included), listIDs(before)) {
		t.Fatalf("include must keep the order %v vs %v", listIDs(included), listIDs(before))
	}
	for _, g := range included.Items {
		if g.ID != f.secondGoal && g.Hidden {
			t.Fatalf("%s marked hidden", g.Key)
		}
	}
	if r := goalRequest(t.Context(), m, f.writer, "GET", "/api/goals?hidden=all", "", ""); r.Code != 400 {
		t.Fatalf("unknown hidden value %d", r.Code)
	}

	// Person B still sees G, cannot read A's row, and B's own hide never reaches A.
	if found, hidden := hiddenOf(list(f.reader, ""), f.secondGoal); !found || hidden {
		t.Fatal("A's hide changed B's view")
	}
	if hiddenRows(t, f, f.reader) != 0 || hiddenRows(t, f, f.writer) != 1 {
		t.Fatal("RLS: B reads A's hidden row, or A cannot read their own")
	}
	hiddenWrite(t, goalRequest(t.Context(), m, f.reader, "PUT", "/api/me/goals/GOAL-1/hidden", "", ""), 200)
	if found, _ := hiddenOf(list(f.writer, ""), f.goal); !found {
		t.Fatal("B's hide changed A's view")
	}
	hiddenWrite(t, goalRequest(t.Context(), m, f.reader, "PUT", "/api/me/goals/GOAL-5/hidden", "", ""), 200)
	if got := list(f.reader, ""); *got.HiddenCount != 2 {
		t.Fatalf("B's count %v", *got.HiddenCount)
	}
	// GOAL-5 (no items) moves to a project B cannot read: it leaves B's count, and B cannot hide it again.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.emptyGoal, f.otherProject)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := list(f.reader, "?hidden=include"); *got.HiddenCount != 1 {
		t.Fatalf("an unreadable goal counts for B: %v", *got.HiddenCount)
	} else if found, _ := hiddenOf(got, f.emptyGoal); found {
		t.Fatal("fixture: B still reads GOAL-5")
	}
	hiddenWrite(t, goalRequest(t.Context(), m, f.reader, "PUT", "/api/me/goals/GOAL-5/hidden", "", ""), 404)
	hiddenWrite(t, goalRequest(t.Context(), m, f.writer, "PUT", "/api/me/goals/TKT-1/hidden", "", ""), 404)

	// Agents and API keys can neither hide nor unhide, for anyone.
	agent := tenant.Principal{TenantID: f.writer.TenantID, Kind: tenant.Agent}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Hide agent') RETURNING id::text`, f.writer.TenantID).Scan(&agent.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, agent.TenantID, agent.ID, "admin")
	keyed := f.writer
	keyed.KeyID, keyed.AuthKeyID = "00000000-0000-4000-8000-0000000000aa", "00000000-0000-4000-8000-0000000000aa"
	bound := agent
	bound.KeyPersonID = f.writer.ID
	for _, p := range []tenant.Principal{agent, keyed, bound} {
		for _, method := range []string{"PUT", "DELETE"} {
			hiddenWrite(t, goalRequest(t.Context(), m, p, method, hide, "", ""), 403)
		}
	}
	if hiddenRows(t, f, agent) != 0 {
		t.Fatal("an agent reads people's hidden rows")
	}

	// Another tenant sees none of these rows and cannot reach this tenant's goal.
	other := tenant.Principal{Kind: tenant.Person}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('goals-hide-other','Other') RETURNING id::text`).Scan(&other.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other admin') RETURNING id::text`, other.TenantID).Scan(&other.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "admin")
	hiddenWrite(t, goalRequest(t.Context(), m, other, "PUT", "/api/me/goals/"+f.secondGoal+"/hidden", "", ""), 404)
	if hiddenRows(t, f, other) != 0 {
		t.Fatal("tenant isolation: another tenant reads hidden rows")
	}

	// Undo: A unhides G, idempotently, and G is back in the same place.
	back := hiddenWrite(t, goalRequest(t.Context(), m, f.writer, "DELETE", hide, "", ""), 200)
	if back.Hidden || !back.Changed {
		t.Fatalf("unhide %+v", back)
	}
	if again := hiddenWrite(t, goalRequest(t.Context(), m, f.writer, "DELETE", hide, "", ""), 200); again.Changed {
		t.Fatalf("unhiding twice must be a no-op %+v", again)
	}
	restored := list(f.writer, "")
	if !slices.Equal(listIDs(restored), listIDs(before)) || *restored.HiddenCount != 0 {
		t.Fatalf("undo did not restore G in place %v vs %v", listIDs(restored), listIDs(before))
	}

	// A goal deleted for good drops its hidden rows.
	hiddenWrite(t, goalRequest(t.Context(), m, f.writer, "PUT", "/api/me/goals/GOAL-5/hidden", "", ""), 200)
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM nodes WHERE id=$1`, f.emptyGoal)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if hiddenRows(t, f, f.writer) != 0 {
		t.Fatal("a deleted goal kept its hidden row")
	}
}
