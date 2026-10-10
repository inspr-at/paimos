// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Overlapping Work parents must contribute each descendant once, including
// paths through legacy/non-work kinds, and never expand an unrelated project.
func TestListEpicMembershipSetBasedAndScoped(t *testing.T) {
	p := newPrincipal(t, "epic-membership")
	customKind(t, p, "task", "task")
	project, work, task, memory := kindBySlug(t, p, "project"), kindBySlug(t, p, "work"), kindBySlug(t, p, "task"), kindBySlug(t, p, "memory")
	create := func(kind, key, parent string) nodeJSON {
		t.Helper()
		return mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"key":%q,"title":%q,"parent_id":%q}`, kind, key, key, parent))
	}
	root := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Membership"}`, project.ID))
	a := create(work.ID, "SET-1", root.ID)
	b := create(work.ID, "SET-2", a.ID)
	c := create(work.ID, "SET-3", b.ID)
	gap := create(memory.ID, "SET-4", b.ID)
	legacy := create(task.ID, "SET-5", gap.ID)
	deep := create(work.ID, "SET-6", legacy.ID)
	loose := create(work.ID, "SET-7", root.ID)
	elsewhere := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Unrelated"}`, project.ID))
	otherParent := create(work.ID, "SET-8", elsewhere.ID)
	_ = create(work.ID, "SET-9", otherParent.ID)

	path := "/api/nodes?within=" + root.ID + "&epic=none&sort=key"
	q, err := parseListQuery(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	sql, args := listFilterSQL(q, false)
	var raw []byte
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON, TIMING OFF) "+sql+` SELECT id FROM filtered`, args...).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var plan any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	found, members := epicMembershipPlanRows(plan)
	if !found || members != 5 {
		t.Fatalf("membership present=%t rows=%.0f, want exactly five scoped descendants once; plan=%v", found, members, safePerformancePlan(plan))
	}

	for _, tc := range []struct {
		filter string
		want   []string
	}{
		{"none", []string{a.Key, loose.Key}},
		{a.ID, []string{b.Key, c.Key, gap.Key, legacy.Key, deep.Key}},
		{b.ID, []string{c.Key, gap.Key, legacy.Key, deep.Key}},
		{a.ID + ",!" + b.ID, []string{b.Key}},
		{"none," + b.ID, []string{a.Key, c.Key, gap.Key, legacy.Key, deep.Key, loose.Key}},
		{"!none", []string{b.Key, c.Key, gap.Key, legacy.Key, deep.Key}},
		{"!" + a.ID, []string{a.Key, loose.Key}},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&epic="+tc.filter+"&sort=key&facets=kind", "")
			page := decode[nodePage](t, status, body, http.StatusOK)
			got := make([]string, len(page.Items))
			for i, item := range page.Items {
				got[i] = item.Key
			}
			if !slices.Equal(got, tc.want) || page.NextCursor != nil {
				t.Fatalf("filter %s: got %v, want %v; cursor=%v", tc.filter, got, tc.want, page.NextCursor)
			}
			total := 0
			for _, count := range page.Facets["kind"] {
				total += count
			}
			if total != len(tc.want) {
				t.Fatalf("facet total=%d, want %d", total, len(tc.want))
			}
		})
	}
	status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+b.ID+"&epic=none", "")
	if page := decode[nodePage](t, status, body, http.StatusOK); len(page.Items) != 0 {
		t.Fatalf("Work scope root must count as a parent: %#v", page.Items)
	}
	foreign := addPrincipal(t, "epic-membership-foreign")
	assertTenantEmpty(t, foreign, path)
	assertTenantEmpty(t, foreign, "/api/nodes?epic="+a.ID)
}

// Scope traversal is not parent membership. Count the actual output of the
// other recursive set, regardless of its name or the planner's join choices.
func epicMembershipPlanRows(value any) (bool, float64) {
	var found bool
	var rows float64
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			present, count := epicMembershipPlanRows(child)
			found = found || present
			rows += count
		}
	case map[string]any:
		if value["Node Type"] == "Recursive Union" && value["Subplan Name"] != "CTE scope" {
			count, _ := value["Actual Rows"].(float64)
			loops, _ := value["Actual Loops"].(float64)
			found, rows = true, count*loops
		}
		for _, key := range []string{"Plan", "Plans"} {
			present, count := epicMembershipPlanRows(value[key])
			found = found || present
			rows += count
		}
	}
	return found, rows
}
