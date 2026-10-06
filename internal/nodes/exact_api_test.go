// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestExactAPISameKindHumanCheck(t *testing.T) {
	p := newPrincipal(t, "same-kind-human-check")
	kind := kindBySlug(t, p, "work")
	for _, alias := range []string{"kind_id", "type", "kind"} {
		t.Run(alias, func(t *testing.T) {
			n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Check"}`, kind.ID))
			value := kind.Slug
			if alias == "kind_id" {
				value = kind.ID
			}
			status, body := call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{%q:%q,"human_check":"Verify"}`, alias, value))
			set := decode[nodeJSON](t, status, body, 200)
			if set.HumanCheck == nil || *set.HumanCheck != "Verify" {
				t.Fatal("same-kind hint discarded human check")
			}
			status, body = call(t, &p, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{%q:%q,"human_check":null}`, alias, value))
			cleared := decode[nodeJSON](t, status, body, 200)
			if cleared.HumanCheck != nil || !strings.Contains(string(cleared.Fields), "human_check_completed") {
				t.Fatal("same-kind hint discarded completion")
			}
		})
	}
}

func TestExactAPIExpiredListCursor(t *testing.T) {
	for _, mutation := range []string{"delete", "filter"} {
		t.Run(mutation, func(t *testing.T) {
			p := newPrincipal(t, "expired-list-"+mutation)
			kind := kindBySlug(t, p, "work")
			for _, title := range []string{"A", "B", "C"} {
				mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":%q,"state":"new"}`, kind.ID, title))
			}
			path := "/api/nodes?kind=ticket&state=new&sort=title&limit=1"
			status, body := call(t, &p, "GET", path, "")
			page := decode[nodePage](t, status, body, 200)
			if len(page.Items) != 1 || page.NextCursor == nil {
				t.Fatal("invalid page fixture")
			}
			if mutation == "delete" {
				status, body = call(t, &p, "DELETE", "/api/nodes/"+page.Items[0].ID, "")
			} else {
				status, body = call(t, &p, "PATCH", "/api/nodes/"+page.Items[0].ID, `{"state":"backlog"}`)
			}
			if status >= 300 {
				t.Fatalf("fixture mutation %d: %s", status, body)
			}
			status, body = call(t, &p, "GET", path+"&cursor="+url.QueryEscape(*page.NextCursor), "")
			if status != 409 || !strings.Contains(string(body), "cursor_expired") {
				t.Fatalf("missing anchor silently restarted: %d %s", status, body)
			}
		})
	}
}

func TestExactAPIGraphUsesConfiguredBuckets(t *testing.T) {
	p := newPrincipal(t, "graph-configured")
	kind := kindBySlug(t, p, "work")
	projectKind := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, projectKind.ID))
	closed := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Shipped","parent_id":%q}`, kind.ID, project.ID))
	qa := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"QA","parent_id":%q}`, kind.ID, project.ID))
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=field_schema||'{"states":[{"state":"shipped","category":"done"}]}'::jsonb WHERE id=$1`, kind.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state=CASE WHEN id=$1::uuid THEN 'shipped' ELSE ' QA ' END WHERE id=ANY($2::uuid[])`, closed.ID, []string{closed.ID, qa.ID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &p, "GET", "/api/tickets/graph?project_id="+project.ID, "")
	graph := decode[TicketGraph](t, status, body, 200)
	if graphHasNode(graph, closed.ID) || graphNode(t, graph, qa.ID).StatusCategory != "doing" {
		t.Fatalf("graph buckets disagree: %s", body)
	}
	status, body = call(t, &p, "GET", "/api/nodes?kind=ticket&within="+project.ID+"&hide_closed=true", "")
	list := decode[nodePage](t, status, body, 200)
	if len(list.Items) != 1 || list.Items[0].ID != qa.ID {
		t.Fatalf("list fixture disagrees: %s", body)
	}
	status, body = call(t, &p, "GET", "/api/tickets/graph?project_id="+project.ID+"&include_closed=true", "")
	graph = decode[TicketGraph](t, status, body, 200)
	if graphNode(t, graph, closed.ID).StatusCategory != "done" {
		t.Fatal("custom done category is not mapped")
	}
}
