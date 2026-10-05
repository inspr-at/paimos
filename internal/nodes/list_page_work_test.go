// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestListDefersUnfilteredAssigneesUntilPage(t *testing.T) {
	p := newPrincipal(t, "page-assignee-work")
	customKind(t, p, "ticket", "ticket")
	person := addPrincipalIn(t, p.TenantID, "Page person")
	project, ticket := kindBySlug(t, p, "project"), kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Page assignees"}`)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,fields)
 SELECT $1,$2,'DEFER-'||g,'Item '||g,'open',$3,
 CASE WHEN g%2=0 THEN jsonb_build_object('assignee_id',$4::text) ELSE '{}'::jsonb END
 FROM generate_series(1,200) g`, p.TenantID, ticket.ID, root.ID, person.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes?within=" + root.ID + "&sort=key&facets=assignee&limit=5"
	status, body := call(t, &p, http.MethodGet, path, "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 5 || page.NextCursor == nil || page.Facets["assignee"][person.ID] != 100 || page.Facets["assignee"]["none"] != 100 {
		t.Fatalf("page/facets lost assignment: %+v", page)
	}
	for _, item := range page.Items {
		n, err := strconv.Atoi(strings.TrimPrefix(item.Key, "DEFER-"))
		if err != nil || n < 1 || n > 5 {
			t.Fatalf("unexpected page key %q", item.Key)
		}
		if n%2 == 0 {
			if item.Assignee == nil || item.Assignee.ID != person.ID || item.Assignee.Name != "Page person" {
				t.Fatal("selected person's assignment was lost")
			}
		} else if item.Assignee != nil {
			t.Fatal("selected unassigned row acquired an assignee")
		}
	}
	plans := logListPerformancePlans(t, p, path)
	if filteredNodeVisits(plans["list"], false) <= 0 {
		t.Fatal("no executed filtered node plan")
	}
	visits := filteredAssigneeVisits(plans["list"], false)
	if visits != 0 {
		t.Fatalf("unfiltered page resolved assignees before selection: %.0f relation visits", visits)
	}
}

func filteredAssigneeVisits(plan any, filtered bool) float64 {
	var visits float64
	switch node := plan.(type) {
	case []any:
		for _, child := range node {
			visits += filteredAssigneeVisits(child, filtered)
		}
	case map[string]any:
		filtered = filtered || node["Subplan Name"] == "CTE filtered"
		if filtered && (node["Relation Name"] == "principals" || node["Relation Name"] == "identities") {
			rows, _ := node["Actual Rows"].(float64)
			removed, _ := node["Rows Removed by Filter"].(float64)
			loops, _ := node["Actual Loops"].(float64)
			visits += (rows + removed) * loops
		}
		visits += filteredAssigneeVisits(node["Plan"], filtered)
		visits += filteredAssigneeVisits(node["Plans"], filtered)
	}
	return visits
}
