// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/principallink"
)

func TestLinkedAssigneesListFacetsPickerAndWrites(t *testing.T) {
	p := newPrincipal(t, "linked-assignees")
	kind := kindBySlug(t, p, "work")
	var alias string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('paimos-classic','link-source:7') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,identity_id) VALUES($1,'person','mba',$2) RETURNING id::text`, p.TenantID, identity).Scan(&alias)
	}); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []string{`{"assignee":"` + alias + `"}`, `{"assignee_id":"` + alias + `"}`, `{"assignee":{"id":"` + alias + `","name":"mba"}}`, `{"classic":{"source_id":"link-source","assignee_id":7}}`, `{"assignee":"` + p.ID + `"}`} {
		mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"Linked","fields":`+fields+`}`)
	}
	service := principallink.New(appPool)
	if _, err := service.Link(t.Context(), "linked-assignees", alias, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"", p.ID, alias} {
		path := "/api/nodes?facets=assignee&limit=2"
		if filter != "" {
			path += "&assignee=" + filter
		}
		status, body := call(t, &p, "GET", path, "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != 2 || len(page.Facets["assignee"]) != 1 || page.Facets["assignee"][p.ID] != 5 {
			t.Fatalf("merged list/facets: %s", body)
		}
		for _, item := range page.Items {
			if item.Assignee == nil || item.Assignee.ID != p.ID || item.Assignee.Name != p.Name {
				t.Fatalf("picker row: %+v", item)
			}
		}
	}
	node := mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"New assignment","fields":{"assignee":"`+alias+`"}}`)
	var fields map[string]any
	_ = json.Unmarshal(node.Fields, &fields)
	if fields["assignee"] != p.ID {
		t.Fatalf("write stored alias: %s", node.Fields)
	}
	status, body := call(t, &p, "PATCH", "/api/nodes/"+node.ID, `{"fields":{"assignee":{"id":"`+alias+`","name":"mba"},"assignee_id":"`+alias+`"}}`)
	updated := decode[nodeJSON](t, status, body, 200)
	_ = json.Unmarshal(updated.Fields, &fields)
	if fields["assignee"].(map[string]any)["id"] != p.ID || fields["assignee"].(map[string]any)["name"] != p.Name || fields["assignee_id"] != p.ID {
		t.Fatalf("patch stored alias: %s", body)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var assignment string
		if err := tx.QueryRow(t.Context(), `SELECT after->'fields'->'assignee'->>'id' FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='node.updated' ORDER BY id DESC LIMIT 1`, p.TenantID, node.ID).Scan(&assignment); err != nil {
			return err
		}
		if assignment != p.ID {
			return fmt.Errorf("event stored alias")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Unlink(t.Context(), "linked-assignees", alias); err != nil {
		t.Fatal(err)
	}
	status, body = call(t, &p, "GET", "/api/nodes?facets=assignee", "")
	page := decode[nodePage](t, status, body, 200)
	if page.Facets["assignee"][alias] != 4 || page.Facets["assignee"][p.ID] != 2 {
		t.Fatalf("unlink rewrote historical assignments: %s", body)
	}
}
